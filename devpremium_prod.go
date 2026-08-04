//go:build production

// Release builds: the dev Premium override does not exist.
package main

func devPremium() bool { return false }
