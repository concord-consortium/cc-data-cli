package auth

import _ "embed"

// logoAsset is the Report Server logo, byte-for-byte the asset the server serves
// at /images/logo.png (a Windows ICO the server labels image/png; browsers render
// it in an <img>). Bundling the same file lets the CLI's login pages match the
// server's own header. Served by the loopback at /logo.png.
//
//go:embed logo.png
var logoAsset []byte
