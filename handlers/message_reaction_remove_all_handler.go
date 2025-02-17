package handlers

import (
	"github.com/disgoorg/disgo/bot"
	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/events"
)

type gatewayHandlerMessageReactionRemoveAll struct{}

func (h *gatewayHandlerMessageReactionRemoveAll) EventType() discord.GatewayEventType {
	return discord.GatewayEventTypeMessageReactionRemoveAll
}

func (h *gatewayHandlerMessageReactionRemoveAll) New() any {
	return &discord.GatewayEventMessageReactionRemoveAll{}
}

func (h *gatewayHandlerMessageReactionRemoveAll) HandleGatewayEvent(client bot.Client, sequenceNumber int, shardID int, v any) {
	messageReaction := *v.(*discord.GatewayEventMessageReactionRemoveAll)

	genericEvent := events.NewGenericEvent(client, sequenceNumber, shardID)

	client.EventManager().DispatchEvent(&events.MessageReactionRemoveAll{
		GenericEvent: genericEvent,
		MessageID:    messageReaction.MessageID,
		ChannelID:    messageReaction.ChannelID,
		GuildID:      messageReaction.GuildID,
	})

	if messageReaction.GuildID == nil {
		client.EventManager().DispatchEvent(&events.DMMessageReactionRemoveAll{
			GenericEvent: genericEvent,
			MessageID:    messageReaction.MessageID,
			ChannelID:    messageReaction.ChannelID,
		})
	} else {
		client.EventManager().DispatchEvent(&events.GuildMessageReactionRemoveAll{
			GenericEvent: genericEvent,
			MessageID:    messageReaction.MessageID,
			ChannelID:    messageReaction.ChannelID,
			GuildID:      *messageReaction.GuildID,
		})
	}
}
