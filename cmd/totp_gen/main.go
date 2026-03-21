package main

import (
	"fmt"

	"github.com/pquerna/otp/totp"
)

func main() {
	key, err := totp.Generate(totp.GenerateOpts{
		Issuer:      "asagumo",
		AccountName: "bot",
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(key.Secret())
}
