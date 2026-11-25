package main

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"os"

	"enhancedkimi/internal/plugins"
	"enhancedkimi/internal/updater"
	bundled "enhancedkimi/plugins"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	key, err := plugins.ParseOfficialPrivateKey(os.Getenv("RELEASE_SIGNING_PRIVATE_KEY"))
	if err != nil {
		return errors.New("RELEASE_SIGNING_PRIVATE_KEY is not a valid Ed25519 seed or private key")
	}
	if os.Getenv("RELEASE_REQUIRE_SIGNING") == "1" {
		public := bundled.OfficialPublicKeys()[bundled.OfficialSigningKeyID]
		if !key.Public().(ed25519.PublicKey).Equal(public) {
			return errors.New("release signing key does not match the embedded official public key")
		}
	}
	body, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}
	signed, err := updater.SignReleaseManifest(body, bundled.OfficialSigningKeyID, key)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(signed)
	return err
}
