// Command vapid-keys prints a new web push (VAPID) key pair for the API's
// VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY settings.
//
// Rotating the pair invalidates every existing browser subscription; users
// enable notifications again from Settings.
package main

import (
	"fmt"
	"os"

	"github.com/dublyo/mailat/api/internal/provider"
)

func main() {
	public, private, err := provider.GenerateVAPIDKeys()
	if err != nil {
		fmt.Fprintln(os.Stderr, "generate VAPID keys:", err)
		os.Exit(1)
	}
	fmt.Printf("VAPID_PUBLIC_KEY=%s\nVAPID_PRIVATE_KEY=%s\nVAPID_SUBJECT=mailto:admin@example.com\n", public, private)
}
