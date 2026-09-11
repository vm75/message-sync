package telegram

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	telegrambotmodels "github.com/go-telegram/bot/models"
	gotdunpack "github.com/gotd/td/telegram/message/unpack"
	"github.com/gotd/td/tg"
	"github.com/vm75/message-sync/internal/transport"
)

const mtprotoPollCorrelationLimit = 4096

type mtprotoPollState struct {
	RemoteID   string   `json:"remoteId"`
	MessageID  int      `json:"messageId"`
	TopicID    int      `json:"topicId,omitempty"`
	OptionKeys []string `json:"optionKeys,omitempty"`
}

type mtprotoPollSendResult struct {
	MessageID  int
	PollID     int64
	OptionKeys []string
}

type mtprotoPollClient interface {
	SendPoll(context.Context, mtprotoPeerState, string, []string, int, int, int, int) (mtprotoPollSendResult, error)
}

func (a *MTProtoAdapter) mtprotoPollProviderNamespace() string {
	if a == nil {
		return "telegram:"
	}
	return "telegram:" + a.connectionID
}

func mtprotoPollOptionKeys(poll tg.Poll) []string {
	keys := make([]string, 0, len(poll.Answers))
	for _, class := range poll.Answers {
		answer, ok := class.(*tg.PollAnswer)
		if !ok || answer == nil || len(answer.Option) == 0 {
			return nil
		}
		keys = append(keys, base64.RawStdEncoding.EncodeToString(answer.Option))
	}
	return keys
}

func mtprotoSyntheticPoll(media *tg.MessageMediaPoll) (*telegrambotmodels.Poll, bool) {
	if media == nil || media.Poll.ID == 0 || media.Poll.Quiz {
		return nil, false
	}
	question := strings.TrimSpace(media.Poll.Question.Text)
	if utf8.RuneCountInString(question) < 1 || utf8.RuneCountInString(question) > 300 || len(media.Poll.Answers) < 2 || len(media.Poll.Answers) > 10 {
		return nil, false
	}
	options := make([]telegrambotmodels.PollOption, 0, len(media.Poll.Answers))
	for _, class := range media.Poll.Answers {
		answer, ok := class.(*tg.PollAnswer)
		if !ok || answer == nil {
			return nil, false
		}
		text := strings.TrimSpace(answer.Text.Text)
		if utf8.RuneCountInString(text) < 1 || utf8.RuneCountInString(text) > 100 || len(answer.Option) == 0 {
			return nil, false
		}
		options = append(options, telegrambotmodels.PollOption{Text: text})
	}
	return &telegrambotmodels.Poll{
		ID: strconv.FormatInt(media.Poll.ID, 10), Question: question, Options: options,
		Type: "regular", AllowsMultipleAnswers: media.Poll.MultipleChoice,
	}, true
}

func mtprotoNativePoll(outgoing transport.Outgoing) (string, []string, int, int, bool) {
	question := strings.TrimSpace(outgoing.SourceText)
	if utf8.RuneCountInString(question) < 1 || utf8.RuneCountInString(question) > 300 || len(outgoing.PollOptions) < 2 || len(outgoing.PollOptions) > 10 {
		return "", nil, 0, 0, false
	}
	selectable := outgoing.PollSelectableCount
	if selectable != 1 && selectable != len(outgoing.PollOptions) {
		return "", nil, 0, 0, false
	}
	duration := outgoing.PollDurationHours * 3600
	if outgoing.PollDurationHours < 0 || (duration > 0 && (duration < 5 || duration > 600)) {
		return "", nil, 0, 0, false
	}
	options := make([]string, 0, len(outgoing.PollOptions))
	for _, raw := range outgoing.PollOptions {
		option := strings.TrimSpace(raw)
		if utf8.RuneCountInString(option) < 1 || utf8.RuneCountInString(option) > 100 {
			return "", nil, 0, 0, false
		}
		options = append(options, option)
	}
	return question, options, selectable, duration, true
}

func (a *MTProtoAdapter) rememberMTProtoPoll(ctx context.Context, pollID int64, value mtprotoPollState) error {
	if a == nil || a.state == nil || pollID == 0 || strings.TrimSpace(value.RemoteID) == "" || value.MessageID <= 0 || len(value.OptionKeys) < 2 {
		return nil
	}
	key := strconv.FormatInt(pollID, 10)
	return a.state.update(ctx, func(state *mtprotoState) {
		if state.Polls == nil {
			state.Polls = make(map[string]mtprotoPollState)
		}
		if len(state.Polls) >= mtprotoPollCorrelationLimit {
			for existing := range state.Polls {
				if existing != key {
					delete(state.Polls, existing)
					break
				}
			}
		}
		value.OptionKeys = append([]string(nil), value.OptionKeys...)
		state.Polls[key] = value
	})
}

func (a *MTProtoAdapter) mtprotoPollCorrelation(ctx context.Context, pollID int64) (mtprotoPollState, bool) {
	if a == nil || a.state == nil || pollID == 0 {
		return mtprotoPollState{}, false
	}
	state, err := a.state.load(ctx)
	if err != nil || state.Polls == nil {
		return mtprotoPollState{}, false
	}
	value, ok := state.Polls[strconv.FormatInt(pollID, 10)]
	if !ok {
		return mtprotoPollState{}, false
	}
	value.OptionKeys = append([]string(nil), value.OptionKeys...)
	return value, true
}

func mtprotoPollStateFromUpdate(update *tg.UpdateMessagePoll) (mtprotoPollState, bool) {
	if update == nil || update.PollID == 0 {
		return mtprotoPollState{}, false
	}
	peer, peerOK := update.GetPeer()
	messageID, messageOK := update.GetMsgID()
	if !peerOK || !messageOK || messageID <= 0 {
		return mtprotoPollState{}, false
	}
	remote, _, ok := mtprotoRemoteIDFromPeer(peer)
	if !ok {
		return mtprotoPollState{}, false
	}
	value := mtprotoPollState{RemoteID: strconv.FormatInt(remote, 10), MessageID: messageID}
	if topicID, ok := update.GetTopMsgID(); ok && topicID > 0 {
		value.TopicID = topicID
	}
	if poll, ok := update.GetPoll(); ok {
		value.OptionKeys = mtprotoPollOptionKeys(poll)
	}
	return value, true
}

func mtprotoPollSnapshot(results tg.PollResults, keys []string) (map[int]int, bool) {
	if len(keys) < 2 {
		return nil, false
	}
	indexes := make(map[string]int, len(keys))
	counts := make(map[int]int, len(keys))
	for index, key := range keys {
		indexes[key] = index
		counts[index] = 0
	}
	for _, result := range results.Results {
		index, found := indexes[base64.RawStdEncoding.EncodeToString(result.Option)]
		if !found {
			continue
		}
		counts[index] = result.Voters
	}
	return counts, true
}

func (a *MTProtoAdapter) handleMTProtoPollUpdate(ctx context.Context, update *tg.UpdateMessagePoll) {
	if a == nil || a.live == nil || update == nil || update.PollID == 0 {
		return
	}
	correlation, haveCorrelation := a.mtprotoPollCorrelation(ctx, update.PollID)
	if current, ok := mtprotoPollStateFromUpdate(update); ok {
		if len(current.OptionKeys) == 0 && haveCorrelation {
			current.OptionKeys = append([]string(nil), correlation.OptionKeys...)
		}
		correlation = current
		haveCorrelation = len(correlation.OptionKeys) >= 2
		if haveCorrelation {
			_ = a.rememberMTProtoPoll(ctx, update.PollID, correlation)
		}
	} else if poll, ok := update.GetPoll(); ok && haveCorrelation {
		if keys := mtprotoPollOptionKeys(poll); len(keys) >= 2 {
			correlation.OptionKeys = keys
			_ = a.rememberMTProtoPoll(ctx, update.PollID, correlation)
		}
	}
	if !haveCorrelation || len(correlation.OptionKeys) < 2 {
		return
	}
	remote, err := strconv.ParseInt(correlation.RemoteID, 10, 64)
	if err != nil {
		return
	}
	a.live.mu.RLock()
	normalizer := a.live.normalizer
	a.live.mu.RUnlock()
	if normalizer == nil {
		return
	}
	endpoint, ok := normalizer.endpoint(remote)
	if !ok {
		return
	}
	snapshot, ok := mtprotoPollSnapshot(update.Results, correlation.OptionKeys)
	if !ok {
		return
	}
	var child *transport.ChildScope
	if correlation.TopicID > 0 {
		child = &transport.ChildScope{Kind: transport.ScopeKindTelegramTopic, RemoteID: strconv.Itoa(correlation.TopicID)}
	}
	a.emitMTProto(transport.Incoming{
		Endpoint: endpoint, RemoteID: strconv.FormatInt(update.PollID, 10), Kind: "poll_snapshot",
		PollSnapshot: snapshot, PollProvider: a.mtprotoPollProviderNamespace(), PollProviderReference: strconv.FormatInt(update.PollID, 10),
		ChildScope: child, Timestamp: time.Now().UTC(),
	})
}

func (c *gotdAuthClient) SendPoll(ctx context.Context, peer mtprotoPeerState, question string, options []string, selectableCount, duration, replyID, topicID int) (mtprotoPollSendResult, error) {
	input, err := peer.input()
	if err != nil {
		return mtprotoPollSendResult{}, err
	}
	pollID, err := mtprotoRandomID()
	if err != nil {
		return mtprotoPollSendResult{}, err
	}
	randomID, err := mtprotoRandomID()
	if err != nil {
		return mtprotoPollSendResult{}, err
	}
	answers := make([]tg.PollAnswerClass, 0, len(options))
	keys := make([]string, 0, len(options))
	for index, option := range options {
		token := []byte(strconv.Itoa(index))
		answers = append(answers, &tg.PollAnswer{Text: tg.TextWithEntities{Text: option}, Option: token})
		keys = append(keys, base64.RawStdEncoding.EncodeToString(token))
	}
	poll := tg.Poll{
		ID: pollID, Question: tg.TextWithEntities{Text: question}, Answers: answers,
		MultipleChoice: selectableCount > 1,
	}
	if duration > 0 {
		poll.ClosePeriod = duration
	}
	updates, err := tg.NewClient(c.client).MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
		Peer: input, ReplyTo: mtprotoReply(replyID, topicID), Media: &tg.InputMediaPoll{Poll: poll}, RandomID: randomID,
	})
	messageID, err := gotdunpack.MessageID(updates, err)
	if err != nil {
		return mtprotoPollSendResult{}, err
	}
	return mtprotoPollSendResult{MessageID: messageID, PollID: pollID, OptionKeys: keys}, nil
}

var _ mtprotoPollClient = (*gotdAuthClient)(nil)
var _ = errors.Is
