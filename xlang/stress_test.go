package xlang_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/neelance/parallel"
	"github.com/sourcegraph/ctxvfs"
	"github.com/sourcegraph/jsonrpc2"
	"github.com/sourcegraph/sourcegraph-go/pkg/lsp"
	"sourcegraph.com/sourcegraph/sourcegraph/xlang"
	"sourcegraph.com/sourcegraph/sourcegraph/xlang/uri"
	"sourcegraph.com/sourcegraph/sourcegraph/xlang/vfsutil"
)

// BenchmarkStress benchmarks performing "textDocument/definition",
// "textDocument/hover", and "textDocument/references" on many
// character positions in many files (that match a specified glob) in
// the workspace.
//
// See the doc comment on BenchmarkIntegration for how to compare
// pre/post benchmark measurements.
func BenchmarkStress(b *testing.B) {
	if testing.Short() {
		b.Skip("skip long integration test")
	}

	orig := xlang.LogTrackedErrors
	xlang.LogTrackedErrors = false
	defer func() {
		xlang.LogTrackedErrors = orig
	}()

	{
		// Serve repository data from codeload.github.com for
		// test performance instead of from gitserver. This
		// technically means we aren't testing gitserver, but
		// that is well tested separately, and the benefit of
		// fast tests here outweighs the benefits of a coarser
		// integration test.
		orig := xlang.NewRemoteRepoVFS
		xlang.NewRemoteRepoVFS = func(cloneURL *url.URL, rev string) (ctxvfs.FileSystem, error) {
			fullName := cloneURL.Host + strings.TrimSuffix(cloneURL.Path, ".git") // of the form "github.com/foo/bar"
			return vfsutil.NewGitHubRepoVFS(fullName, rev, "", true)
		}
		defer func() {
			xlang.NewRemoteRepoVFS = orig
		}()
	}

	tests := map[string]struct { // map key is rootPath
		mode    string
		fileExt string
	}{
		"git://github.com/gorilla/mux?0a192a193177452756c362c20087ddafcf6829c4": {
			mode:    "go",
			fileExt: ".go",
		},
		"git://github.com/gorilla/csrf?a8abe8abf66db8f4a9750d76ba95b4021a354757": {
			mode:    "go",
			fileExt: ".go",
		},
		"git://github.com/golang/go?go1.7.1": {
			mode:    "go",
			fileExt: ".go",
		},
	}
	for rootPath, test := range tests {
		root, err := uri.Parse(rootPath)
		if err != nil {
			b.Fatal(err)
		}
		label := strings.Replace(root.Host+root.Path, "/", "-", -1)

		b.Run(label, func(b *testing.B) {
			fs, err := xlang.NewRemoteRepoVFS(root.CloneURL(), root.Rev())
			if err != nil {
				b.Fatal(err)
			}
			fs.Stat(context.Background(), ".") // ensure repo archive has been fetched and read before starting timer

			allFiles, err := ctxvfs.ReadAllFiles(context.Background(), fs, "/", func(fi os.FileInfo) bool {
				return fi.Mode().IsRegular() && path.Ext(fi.Name()) == test.fileExt
			})
			if err != nil {
				b.Fatal(err)
			}

			// Sort filenames for determinism.
			filenames := make([]string, 0, len(allFiles))
			for f := range allFiles {
				filenames = append(filenames, f)
			}
			sort.Strings(filenames)

			allFileCharPos := make(map[string][][2]int, len(allFiles)) // all possible character pos (not byte pos)
			maxFiles := 10
			for _, path := range filenames {
				maxFiles--
				if maxFiles < 0 {
					break
				}
				maxPos := 10

				contentsBytes := allFiles[path]
				n := utf8.RuneCount(contentsBytes)
				allFileCharPos[path] = make([][2]int, 0, n)

				line := 0
				character := 0
				for i, r := range string(contentsBytes) {
					if i >= maxPos {
						break
					}

					allFileCharPos[path] = append(allFileCharPos[path], [2]int{line, character})
					if string(r) == "\n" {
						line++
						character = 0
					} else {
						character++
					}
				}
			}

			ctx := context.Background()
			proxy := xlang.NewProxy()
			addr, done := startProxy(b, proxy)
			defer done()

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				// Don't measure the time it takes to dial and
				// initialize, because this is amortized over each
				// operation we do.
				c := dialProxy(b, addr, nil)
				if err := c.Call(ctx, "initialize", xlang.ClientProxyInitializeParams{
					InitializeParams: lsp.InitializeParams{RootPath: root.String()},
					Mode:             test.mode,
				}, nil); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()

				var wg sync.WaitGroup
				for path, pos := range allFileCharPos {
					for _, p := range pos {
						wg.Add(1)
						go func(path string, line, character int) {
							defer wg.Done()
							if err := doStressTestOpForPosition(ctx, c, root, path, line, character); err != nil {
								if !strings.Contains(err.Error(), "invalid location:") { // harmless error
									b.Logf("%s:%d:%d: %s", path, line, character, err)
								}
							}
						}(path, p[0], p[1])
					}
				}
				wg.Wait()

				// If we don't shut down the server, then subsequent
				// iterations will test the performance when it's
				// already cached, which is not what we want.
				b.StopTimer()
				proxy.ShutDownIdleServers(ctx, 0)
				b.StartTimer()
			}
			b.StopTimer() // don't include server teardown
		})
	}
}

func doStressTestOpForPosition(ctx context.Context, c *jsonrpc2.Conn, root *uri.URI, path string, line, character int) error {
	params := lsp.TextDocumentPositionParams{
		TextDocument: lsp.TextDocumentIdentifier{URI: root.WithFilePath(path).String()},
		Position:     lsp.Position{Line: line, Character: character},
	}
	methods := []string{"textDocument/definition", "textDocument/hover", "textDocument/references"}
	par := parallel.NewRun(len(methods))
	for _, method := range methods {
		par.Acquire()
		go func(method string) {
			defer par.Release()
			if err := c.Call(ctx, method, params, nil); err != nil {
				par.Error(fmt.Errorf("%s: %s", method, err))
			}
		}(method)
	}
	return par.Wait()
}
