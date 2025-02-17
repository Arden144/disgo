package handlers

import (
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
)

type gatewayHandlerGuildScheduledEventCreate struct{}

func (h *gatewayHandlerGuildScheduledEventCreate) EventType() discord.GatewayEventType {
	return discord.GatewayEventTypeGuildScheduledEventCreate
}

func (h *gatewayHandlerGuildScheduledEventCreate) New() any {
	return &discord.GuildScheduledEvent{}
}

func (h *gatewayHandlerGuildScheduledEventCreate) HandleGatewayEvent(client bot.Client, sequenceNumber int, shardID int, v any) {
	guildScheduledEvent := *v.(*discord.GuildScheduledEvent)

	client.Caches().GuildScheduledEvents().Put(guildScheduledEvent.GuildID, guildScheduledEvent.ID, guildScheduledEvent)

	client.EventManager().DispatchEvent(&events.GuildScheduledEventCreate{
		GenericGuildScheduledEvent: &events.GenericGuildScheduledEvent{
			GenericEvent:   events.NewGenericEvent(client, sequenceNumber, shardID),
			GuildScheduled: guildScheduledEvent,
		},
	})
}
