// Package openapi は OpenAPI の定義（API の仕様の正本）を Go に埋め込む。
package openapi

import (
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"path"

	"github.com/getkin/kin-openapi/openapi3"
)

// FS は全 audience の定義。
//
//go:embed common.yaml */*.yaml
var FS embed.FS

// Load は name（例 "dev/platform.yaml"）を $ref を解決して読む。
func Load(name string) (*openapi3.T, error) {
	l := openapi3.NewLoader()
	l.IsExternalRefsAllowed = true
	l.ReadFromURIFunc = func(_ *openapi3.Loader, u *url.URL) ([]byte, error) {
		return fs.ReadFile(FS, path.Clean(u.Path))
	}
	s, err := l.LoadFromURI(&url.URL{Path: name})
	if err != nil {
		return nil, fmt.Errorf("openapi: load %s: %w", name, err)
	}
	if err := s.Validate(l.Context); err != nil {
		return nil, fmt.Errorf("openapi: validate %s: %w", name, err)
	}
	return s, nil
}
