package telegram

import (
	"errors"
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"
	"github.com/vm75/message-sync/internal/config"
	"github.com/vm75/message-sync/internal/identity"
)

func telegramPollText(question string, options []string, selectableCount int) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", errors.New("outgoing Telegram poll question is required")
	}
	if len(options) == 0 {
		return "", errors.New("outgoing Telegram poll options are required")
	}

	var builder strings.Builder
	builder.WriteString("Poll: ")
	builder.WriteString(question)
	for i, option := range options {
		builder.WriteString("\n")
		builder.WriteString(fmt.Sprintf("%d. %s", i+1, strings.TrimSpace(option)))
	}
	if selectableCount > 1 {
		if selectableCount > len(options) {
			selectableCount = len(options)
		}
		builder.WriteString(fmt.Sprintf("\nChoose up to %d options.", selectableCount))
	} else {
		builder.WriteString("\nChoose one option.")
	}
	return builder.String(), nil
}

func telegramPollMessageText(poll *models.Poll, hasher *identity.Hasher, usernameMode config.UsernameMode) (string, bool) {
	if poll == nil || telegramPollHasSensitiveLocation(poll) {
		return "", false
	}
	question, _ := normalizeTelegramMentions(poll.Question, poll.QuestionEntities, hasher, usernameMode)
	options := make([]string, 0, len(poll.Options))
	for _, option := range poll.Options {
		optionText, _ := normalizeTelegramMentions(option.Text, option.TextEntities, hasher, usernameMode)
		options = append(options, optionText)
	}
	selectableCount := 1
	if poll.AllowsMultipleAnswers {
		selectableCount = len(options)
	}
	text, err := telegramPollText(question, options, selectableCount)
	return text, err == nil
}

func telegramPollHasSensitiveLocation(poll *models.Poll) bool {
	if poll == nil {
		return false
	}
	sensitiveMedia := func(media *models.PollMedia) bool {
		return media != nil && (media.Location != nil || media.Venue != nil)
	}
	if sensitiveMedia(poll.Media) || sensitiveMedia(poll.ExplanationMedia) {
		return true
	}
	for _, option := range poll.Options {
		if sensitiveMedia(option.Media) {
			return true
		}
	}
	return false
}

func telegramSensitivePayload(message *models.Message) bool {
	if message == nil {
		return false
	}
	return message.Contact != nil || message.Location != nil || message.Venue != nil
}

func ignoredTelegramMessageClass(message *models.Message) (string, bool) {
	if message == nil {
		return "", false
	}
	switch {
	case message.Contact != nil:
		return "contact", true
	case message.Location != nil || message.Venue != nil:
		return "location", true
	case len(message.NewChatMembers) > 0 || message.LeftChatMember != nil:
		return "membership", true
	case message.NewChatTitle != "" || len(message.NewChatPhoto) > 0 || message.DeleteChatPhoto:
		return "chat_metadata", true
	case message.PinnedMessage != nil:
		return "pin", true
	case message.Invoice != nil || message.SuccessfulPayment != nil || message.RefundedPayment != nil:
		return "payment", true
	case message.Game != nil:
		return "game", true
	case message.ForumTopicCreated != nil || message.ForumTopicEdited != nil ||
		message.ForumTopicClosed != nil || message.ForumTopicReopened != nil ||
		message.GeneralForumTopicHidden != nil || message.GeneralForumTopicUnhidden != nil:
		return "forum_service", true
	case strings.TrimSpace(message.Text) == "" && strings.TrimSpace(message.Caption) == "" &&
		message.Poll == nil && !hasTelegramMedia(message):
		return "unsupported", true
	default:
		return "", false
	}
}
