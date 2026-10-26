// Package manifests embeds the same plain YAML files shipped for students.
package manifests

import "embed"

// Files contains the visible lab manifests.
//
//go:embed autoscaler/*.yaml load.yaml
var Files embed.FS
