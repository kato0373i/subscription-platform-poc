//go:build devtools

package config

// DevtoolsBuild は -tags devtools でビルドしたか。dev clock・/v1/dev/*・開発用 JWT を含む。
const DevtoolsBuild = true
