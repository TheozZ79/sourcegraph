package config

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

const yamlTestInput = `
shared_steps:
  - root: /
    image: node:12
    commands:
      - yarn install --frozen-lockfile --non-interactive

index_jobs:
  -
    steps:
      - image: go:latest
        commands:
          - go mod vendor
    indexer: lsif-go
    indexer_args:
      - --no-animation
  -
    root: web/
    indexer: lsif-tsc
    indexer_args: ['-p', '.']
    outfile: lsif.dump
`

func TestUnmarshalYAML(t *testing.T) {
	actual, err := UnmarshalYAML([]byte(yamlTestInput))
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	expected := IndexConfiguration{
		SharedSteps: []DockerStep{
			{
				Root:     "/",
				Image:    "node:12",
				Commands: []string{"yarn install --frozen-lockfile --non-interactive"},
			},
		},
		IndexJobs: []IndexJob{
			{
				Steps: []DockerStep{
					{
						Root:     "",
						Image:    "go:latest",
						Commands: []string{"go mod vendor"},
					},
				},
				Indexer:     "lsif-go",
				IndexerArgs: []string{"--no-animation"},
			},
			{
				Steps:       nil,
				Root:        "web/",
				Indexer:     "lsif-tsc",
				IndexerArgs: []string{"-p", "."},
				Outfile:     "lsif.dump",
			},
		},
	}
	if diff := cmp.Diff(expected, actual); diff != "" {
		t.Errorf("unexpected configuration (-want +got):\n%s", diff)
	}
}
