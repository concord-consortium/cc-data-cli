package creds

import "github.com/zalando/go-keyring"

// keyringService is the OS keychain service name; the account is the portal host.
const keyringService = "cc-data"

// Function seams over go-keyring so tests can force a backend outcome.
var (
	keyringSet    = keyring.Set
	keyringGet    = keyring.Get
	keyringDelete = keyring.Delete
)

// keyringErrNotFound is go-keyring's "secret not in keychain" sentinel.
var keyringErrNotFound = keyring.ErrNotFound
