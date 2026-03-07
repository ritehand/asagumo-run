module github.com/ritehand/asagumo-run

go 1.24.0

replace github.com/bwmarrin/discordgo => github.com/1l0/discordgo-fork v0.0.0-20260307091336-e7e846bd555e

// replace github.com/bwmarrin/discordgo => ../../yeongaori/discordgo-fork

require (
	github.com/bwmarrin/discordgo v0.29.0
	github.com/ritehand/asagumo v0.1.1
)

require (
	github.com/cloudflare/circl v1.6.3 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
)
