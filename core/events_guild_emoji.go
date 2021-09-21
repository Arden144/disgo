package core

// GenericEmojiEvent is called upon receiving EmojiCreateEvent, EmojiUpdateEvent or EmojiDeleteEvent(requires core.GatewayIntentsGuildEmojis)
type GenericEmojiEvent struct {
	*GenericGuildEvent
	Emoji *Emoji
}

// EmojiCreateEvent indicates that a new core.Emoji got created in an core.Guild(requires core.GatewayIntentsGuildEmojis)
type EmojiCreateEvent struct {
	*GenericEmojiEvent
}

// EmojiUpdateEvent indicates that an core.Emoji got updated in an core.Guild(requires core.GatewayIntentsGuildEmojis)
type EmojiUpdateEvent struct {
	*GenericEmojiEvent
	OldEmoji *Emoji
}

// EmojiDeleteEvent indicates that an core.Emoji got deleted in an core.Guild(requires core.GatewayIntentsGuildEmojis)
type EmojiDeleteEvent struct {
	*GenericEmojiEvent
}
