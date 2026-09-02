// keygen prints fresh random keys for local and pilot configuration. It never touches
// files or the network; paste the output into .env or the secret manager.
package main

import (
	"fmt"
	"os"

	"github.com/celikbros/kapsora/internal/platform/crypto/localkey"
)

func main() {
	for _, name := range []string{"KAPSORA_LOCAL_MASTER_KEY", "KAPSORA_COOKIE_SIGNING_KEY"} {
		key, err := localkey.GenerateMasterKey()
		if err != nil {
			fmt.Fprintln(os.Stderr, "keygen:", err)
			os.Exit(1)
		}
		fmt.Printf("%s=%s\n", name, key)
	}
}
