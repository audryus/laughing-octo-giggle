package states

import (
	"context"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"server/internal/server"
	"server/internal/server/db"
	"server/internal/server/objects"
	"server/pkg/packets"
	"time"
)

type InGame struct {
	client                 server.ClientInterfacer
	player                 *objects.Player
	logger                 *log.Logger
	cancelPlayerUpdateLoop context.CancelFunc
}

func (g *InGame) Name() string {
	return "InGame"
}

func (g *InGame) SetClient(client server.ClientInterfacer) {
	g.client = client
	loggingPrefix := fmt.Sprintf("Client %d [%s]: ", client.Id(), g.Name())
	g.logger = log.New(log.Writer(), loggingPrefix, log.LstdFlags)
}

func (g *InGame) OnEnter() {
	g.logger.Printf("Adding player %s to the shared collection", g.player.Name)
	go g.client.SharedGameObjects().Players.Add(g.player, g.client.Id())

	g.player.X, g.player.Y = objects.SpawnCoords(g.player.Radius, g.client.SharedGameObjects().Players, g.client.SharedGameObjects().Spores)
	g.player.Speed = 150.0
	g.player.Radius = 20

	g.client.SocketSend(packets.NewPlayer(g.client.Id(), g.player))

	go g.sendInitialSpores(100, 10*time.Millisecond)
}

func (g *InGame) HandleMessage(senderId uint64, message packets.Msg) {
	switch message := message.(type) {
	case *packets.Packet_Player:
		g.handlePlayer(senderId, message)
	case *packets.Packet_PlayerDirection:
		g.handlePlayerDirection(senderId, message)
	case *packets.Packet_Chat:
		g.handleChat(senderId, message)
	case *packets.Packet_SporeConsumed:
		g.handleSporeConsumed(senderId, message)
	case *packets.Packet_PlayerConsumed:
		g.handlePlayerConsumed(senderId, message)
	case *packets.Packet_Spore:
		g.handleSpore(senderId, message)
	case *packets.Packet_Disconnect:
		g.handleDisconnect(senderId, message)
	}
}

func (g *InGame) OnExit() {
	if g.cancelPlayerUpdateLoop != nil {
		g.cancelPlayerUpdateLoop()
	}

	g.client.SharedGameObjects().Players.Remove(g.client.Id())
	g.syncPlayerBestScore()
}

func (g *InGame) handleChat(senderId uint64, message *packets.Packet_Chat) {
	if senderId == g.client.Id() {
		g.client.Broadcast(message)
	} else {
		g.client.SocketSendAs(message, senderId)
	}
}

func (g *InGame) handlePlayer(senderId uint64, message *packets.Packet_Player) {
	if senderId == g.client.Id() {
		g.logger.Println("Received player message from our own client, ignoring")
		return
	}

	g.client.SocketSendAs(message, senderId)
}

func (g *InGame) handlePlayerDirection(senderId uint64, message *packets.Packet_PlayerDirection) {
	if senderId == g.client.Id() {
		g.player.Direction = message.PlayerDirection.Direction

		if g.cancelPlayerUpdateLoop == nil {
			ctx, cancel := context.WithCancel(context.Background())
			g.cancelPlayerUpdateLoop = cancel
			go g.updatePlayerLoop(ctx)
		}
	}

	g.client.SocketSendAs(message, senderId)
}

func (g *InGame) updatePlayerLoop(ctx context.Context) {
	const delta float64 = 0.05
	ticker := time.NewTicker(time.Duration(delta*1000) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			g.syncPlayer(delta)
		case <-ctx.Done():
			return
		}
	}
}

func (g *InGame) syncPlayer(delta float64) {
	newX := g.player.X + g.player.Speed*math.Cos(g.player.Direction)*delta
	newY := g.player.Y + g.player.Speed*math.Sin(g.player.Direction)*delta

	g.player.X = newX
	g.player.Y = newY

	probability := g.player.Radius / float64(server.MaxSpores*5)
	if rand.Float64() < probability && g.player.Radius > 10 {
		spore := &objects.Spore{
			X:         g.player.X,
			Y:         g.player.Y,
			Radius:    min(5+g.player.Radius/50, 15),
			DroppedBy: g.player,
			DroppedAt: time.Now(),
		}
		sporeId := g.client.SharedGameObjects().Spores.Add(spore)
		g.client.Broadcast(packets.NewSpore(sporeId, spore))
		go g.client.SocketSend(packets.NewSpore(sporeId, spore))

		g.player.Radius = g.nextRadius(-radToMass(spore.Radius))
	}

	updatePacket := packets.NewPlayer(g.client.Id(), g.player)
	g.client.Broadcast(updatePacket)

	go g.client.SocketSend(updatePacket)
}

func (g *InGame) handleSporeConsumed(senderId uint64, message *packets.Packet_SporeConsumed) {
	if senderId != g.client.Id() {
		g.client.SocketSendAs(message, senderId)
		return
	}

	errorsMsg := "Could not verify spore consumption: "

	sporeId := message.SporeConsumed.SporeId
	spore, err := g.getSpore(sporeId)

	if err != nil {
		g.logger.Println(errorsMsg + err.Error())
		return
	}

	err = g.validatePlayerCloseToObject(spore.X, spore.Y, spore.Radius, 10)
	if err != nil {
		g.logger.Println(errorsMsg + err.Error())
		return
	}

	err = g.validatePlayerDropCooldown(spore, 10)
	if err != nil {
		g.logger.Println(errorsMsg + err.Error())
		return
	}

	sporeMass := radToMass(spore.Radius)
	g.player.Radius = g.nextRadius(sporeMass)

	go g.client.SharedGameObjects().Spores.Remove(sporeId)

	g.client.Broadcast(message)

	go g.syncPlayerBestScore()
}

func (g *InGame) handlePlayerConsumed(senderId uint64, message *packets.Packet_PlayerConsumed) {
	if senderId != g.client.Id() {
		g.client.SocketSendAs(message, senderId)

		if message.PlayerConsumed.PlayerId == g.client.Id() {
			g.logger.Println("Player was consumed, respawing")
			g.client.SetState(&InGame{
				player: &objects.Player{
					Name: g.player.Name,
				},
			})
		}

		return
	}

	errorsMsg := "Could not verify player consumption: "

	otherId := message.PlayerConsumed.PlayerId
	other, err := g.getOtherPlayer(otherId)
	if err != nil {
		g.logger.Println(errorsMsg + err.Error())
		return
	}

	ourMass := radToMass(g.player.Radius)
	otherMass := radToMass(other.Radius)

	if ourMass <= otherMass*1.5 {
		g.logger.Printf(errorsMsg+"player not massive enough to consume the other player (our radius: %f, other radius: %f)", g.player.Radius, other.Radius)
		return
	}

	// Finally, check if the player is close enough to the other to be consumed
	err = g.validatePlayerCloseToObject(other.X, other.Y, other.Radius, 10)
	if err != nil {
		g.logger.Println(errorsMsg + err.Error())
		return
	}

	// If we made it this far, the player consumption is valid, so grow the player, remove the consumed other, and broadcast the event
	g.player.Radius = g.nextRadius(otherMass)

	go g.client.SharedGameObjects().Players.Remove(otherId)

	g.client.Broadcast(message)

	go g.syncPlayerBestScore()

}

func (g *InGame) handleDisconnect(senderId uint64, message *packets.Packet_Disconnect) {
	if senderId == g.client.Id() {
		g.client.Broadcast(message)
		g.client.SetState(&Connected{})
		return
	}

	go g.client.SocketSendAs(message, senderId)
}

func (g *InGame) handleSpore(senderId uint64, message *packets.Packet_Spore) {
	g.client.SocketSendAs(message, senderId)
}

func (g *InGame) sendInitialSpores(batchSize int, delay time.Duration) {
	sporesBatch := make(map[uint64]*objects.Spore, batchSize)

	g.client.SharedGameObjects().Spores.ForEach(func(sporeId uint64, spore *objects.Spore) {
		sporesBatch[sporeId] = spore
		if len(sporesBatch) >= batchSize {
			g.client.SocketSend(packets.NewSporeBatch(sporesBatch))
			sporesBatch = make(map[uint64]*objects.Spore, batchSize)
			time.Sleep(delay)
		}
	})

	if len(sporesBatch) > 0 {
		g.client.SocketSend(packets.NewSporeBatch(sporesBatch))
	}
}

func (g *InGame) getSpore(sporeId uint64) (*objects.Spore, error) {
	spore, exists := g.client.SharedGameObjects().Spores.Get(sporeId)

	if !exists {
		return nil, fmt.Errorf("spore with ID %d does not exists", sporeId)
	}

	return spore, nil
}

func (g *InGame) getOtherPlayer(playerId uint64) (*objects.Player, error) {
	player, exists := g.client.SharedGameObjects().Players.Get(playerId)

	if !exists {
		return nil, fmt.Errorf("player with ID %d does not exists", playerId)
	}

	return player, nil
}

func (g *InGame) validatePlayerCloseToObject(objX, objY, objRadius, buffer float64) error {
	realDX := g.player.X - objX
	realDY := g.player.Y - objY
	realDistSq := realDX*realDX + realDY*realDY

	thresholdDist := g.player.Radius + buffer + objRadius
	thresholdDistSq := thresholdDist * thresholdDist

	if realDistSq > thresholdDistSq {
		return fmt.Errorf("player is too far from the object (distSq %f, thresholdSq %f)", realDistSq, thresholdDistSq)
	}

	return nil
}

func (g *InGame) validatePlayerDropCooldown(spore *objects.Spore, buffer float64) error {
	minAcceptableDistance := spore.Radius + g.player.Radius + buffer
	minAcceptableTime := time.Duration(minAcceptableDistance/g.player.Speed*1000) * time.Millisecond
	if spore.DroppedBy == g.player && time.Since(spore.DroppedAt) < minAcceptableTime {
		return fmt.Errorf("player dropped the spore too recently (time %v, min acceptable time: %v)", time.Since(spore.DroppedAt), minAcceptableTime)
	}
	return nil
}

func radToMass(radius float64) float64 {
	return math.Pi * radius * radius
}

func massToRad(mass float64) float64 {
	return math.Sqrt(mass / math.Pi)
}

func (g *InGame) nextRadius(massDiff float64) float64 {
	oldMass := radToMass(g.player.Radius)
	newMass := oldMass + massDiff
	return massToRad(newMass)
}

func (g *InGame) syncPlayerBestScore() {
	currentScore := int64(math.Round(radToMass(g.player.Radius)))
	if currentScore > g.player.BestScore {
		g.player.BestScore = currentScore
		err := g.client.DbTx().Queries.UpdatePlayerBestScore(g.client.DbTx().Ctx, db.UpdatePlayerBestScoreParams{
			ID:        g.player.DbId,
			BestScore: g.player.BestScore,
		})
		if err != nil {
			g.logger.Printf("Error updating player best score: %v", err)
		}
	}
}
