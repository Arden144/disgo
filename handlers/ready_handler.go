package handlers

import (
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
)

type gatewayHandlerReady struct{}

func (h *gatewayHandlerReady) EventType() discord.GatewayEventType {
	return discord.GatewayEventTypeReady
}

func (h *gatewayHandlerReady) New() any {
	return &discord.GatewayEventReady{}
}

func (h *gatewayHandlerReady) HandleGatewayEvent(client bot.Client, sequenceNumber int, _ int, v any) {
	readyEvent := *v.(*discord.GatewayEventReady)

	var shardID int
	if readyEvent.Shard != nil {
		shardID = readyEvent.Shard[0]
	}

	client.Caches().PutSelfUser(readyEvent.User)

	for _, guild := range readyEvent.Guilds {
		client.Caches().Guilds().SetUnready(shardID, guild.ID)
	}

	client.EventManager().DispatchEvent(&events.Ready{
		GenericEvent:      events.NewGenericEvent(client, sequenceNumber, shardID),
		GatewayEventReady: readyEvent,
	})

}
