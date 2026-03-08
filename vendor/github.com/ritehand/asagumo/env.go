package asagumo

import (
	"log"
	"os"
)

var (
	AppID     string
	PublicKey string
	GuildID   string
	Token     string
)

func init() {
	AppID = os.Getenv("ASAGUMO_APP_ID")
	PublicKey = os.Getenv("ASAGUMO_PUBLIC_KEY")
	GuildID = os.Getenv("ASAGUMO_GUILD_ID")
	Token = os.Getenv("ASAGUMO_TOKEN")
	if AppID == "" || PublicKey == "" || GuildID == "" || Token == "" {
		log.Fatalln("Missing environment variables")
	}
}
