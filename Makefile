proto: 
	@protoc -I="shared" --go_out="server" "shared/packets.proto"

sqlc:
	@sqlc generate -f .\server\internal\server\db\config\sqlc.yml