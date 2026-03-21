.PHONY: build run dev

build:
	docker build -t asagumo-run:latest --platform=linux/amd64 .
run:
	docker run --rm -it --platform=linux/amd64 -p 8000:8000 \
	-e ASAGUMO_TOKEN=${ASAGUMO_TOKEN} \
	-e ASAGUMO_GUILD_ID=${ASAGUMO_GUILD_ID} \
	-e DEV=true \
	-e ASAGUMO_TOKEN_DEV=${ASAGUMO_TOKEN_DEV} \
	-e PUBLIC_HOST=${PUBLIC_HOST} \
	-e HUB_SECRET=${HUB_SECRET} \
	-e OTP_SECRET=${OTP_SECRET} \
	-e OTP_ROLE_ID=${OTP_ROLE_ID} \
	asagumo-run:latest
dev: build run
