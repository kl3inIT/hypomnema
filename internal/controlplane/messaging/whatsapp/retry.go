package whatsapp

// A file WhatsApp's servers no longer hold is asked of the phone (issue #651), as wacli recovers expired media
// (internal/wa/mediaretry.go, internal/app/media_retry.go): a media retry receipt asks the phone to upload it again,
// and its MediaRetry event, decrypted with the message's key, gives a new path to download, or says the phone has
// it no more. The phone must be online; one that does not answer in 30 s (wacli's wait) leaves the file for the
// next connection. A direct chat, and a group's sender in a group addressed by LID, are named by LID once the
// account moved to LIDs (wacli rewriteMediaRetryInfoForLID). A waiting download is found by the message's id alone
// (inference: wacli also keys by chat, which here may come back under the chat's other identity).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waMmsRetry"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// mediaRetryWait is how long the phone has to upload a file again (wacli RetryMediaOptions.Wait).
const mediaRetryWait = 30 * time.Second

// errNotOnPhone is a file neither WhatsApp's servers nor the phone hold any more.
var errNotOnPhone = errors.New("whatsapp: neither WhatsApp nor the phone has this file any more")

// expired says WhatsApp's servers no longer serve a file at its path (whatsmeow download.go retries the same).
func expired(err error) bool {
	return errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith403) ||
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith404) ||
		errors.Is(err, whatsmeow.ErrMediaDownloadFailedWith410)
}

// askPhone asks the phone to upload a message's file again and answers its new path.
func (a *account) askPhone(ctx context.Context, job mediaJob, row mediaRow) (string, error) {
	info, err := a.retryInfo(ctx, job, row)
	if err != nil {
		return "", err
	}

	answered := make(chan *events.MediaRetry, 1)

	a.retryMu.Lock()
	a.retrying[job.id] = answered
	a.retryMu.Unlock()

	defer func() {
		a.retryMu.Lock()
		delete(a.retrying, job.id)
		a.retryMu.Unlock()
	}()

	if err := a.client.SendMediaRetryReceipt(ctx, info, row.mediaKey); err != nil {
		return "", fmt.Errorf("whatsapp: asking the phone for the file of message %s: %w", job.id, err)
	}

	var evt *events.MediaRetry

	select {
	case evt = <-answered:
	case <-time.After(mediaRetryWait):
		return "", fmt.Errorf("whatsapp: the phone did not upload the file of message %s again in %s", job.id,
			mediaRetryWait)
	case <-ctx.Done():
		return "", ctx.Err()
	}

	notif, err := whatsmeow.DecryptMediaRetryNotification(evt, row.mediaKey)
	if errors.Is(err, whatsmeow.ErrMediaNotAvailableOnPhone) {
		return "", errNotOnPhone
	}

	if err != nil {
		return "", fmt.Errorf("whatsapp: reading the phone's answer for message %s: %w", job.id, err)
	}

	switch notif.GetResult() {
	case waMmsRetry.MediaRetryNotification_SUCCESS:
		if notif.GetDirectPath() != "" {
			return notif.GetDirectPath(), nil
		}
	case waMmsRetry.MediaRetryNotification_NOT_FOUND:
		return "", errNotOnPhone
	case waMmsRetry.MediaRetryNotification_GENERAL_ERROR, waMmsRetry.MediaRetryNotification_DECRYPTION_ERROR:
	}

	return "", fmt.Errorf("whatsapp: the phone could not upload the file of message %s again (%s)", job.id,
		notif.GetResult())
}

// retried hands a phone's answer to the download waiting for it.
func (a *account) retried(e *events.MediaRetry) {
	a.retryMu.Lock()
	ch := a.retrying[e.MessageID]
	a.retryMu.Unlock()

	if ch != nil {
		select {
		case ch <- e:
		default:
		}
	}
}

// retryInfo names the message as the phone knows it.
func (a *account) retryInfo(ctx context.Context, job mediaJob, row mediaRow) (*types.MessageInfo, error) {
	chat, err := types.ParseJID(job.chat)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: chat %q: %w", job.chat, err)
	}

	sender, _ := types.ParseJID(row.sender)
	if sender.IsEmpty() && row.fromMe {
		if id := a.client.Store.ID; id != nil {
			sender = id.ToNonAD()
		}
	}

	group := chat.Server == types.GroupServer || chat.IsBroadcastList()
	if group && sender.IsEmpty() {
		return nil, fmt.Errorf("whatsapp: message %s of a group has no sender to ask the phone with", job.id)
	}

	mode := types.AddressingModePN

	if chat.Server == types.GroupServer {
		if mode, err = a.groupMode(ctx, chat); err != nil {
			return nil, err
		}
	}

	if a.client.Store.LIDMigrationTimestamp > 0 {
		switch {
		case chat.Server == types.DefaultUserServer:
			chat = a.toLID(ctx, chat)
			sender = a.toLID(ctx, sender)
		case mode == types.AddressingModeLID:
			sender = a.toLID(ctx, sender)
		}
	}

	return &types.MessageInfo{ID: job.id, MessageSource: types.MessageSource{
		Chat: chat, Sender: sender, IsFromMe: row.fromMe, IsGroup: group, AddressingMode: mode,
	}}, nil
}

// groupMode is how a group addresses its members, read once a session.
func (a *account) groupMode(ctx context.Context, group types.JID) (types.AddressingMode, error) {
	a.retryMu.Lock()
	mode, ok := a.groupModes[group]
	a.retryMu.Unlock()

	if ok {
		return mode, nil
	}

	info, err := a.client.GetGroupInfo(ctx, group)
	if err != nil {
		return "", fmt.Errorf("whatsapp: reading how group %s addresses its members: %w", group, err)
	}

	mode = info.AddressingMode
	if mode == "" {
		mode = types.AddressingModePN
	}

	a.retryMu.Lock()
	a.groupModes[group] = mode
	a.retryMu.Unlock()

	return mode, nil
}

// toLID is a number's LID when whatsmeow knows it, else the number.
func (a *account) toLID(ctx context.Context, jid types.JID) types.JID {
	if jid.Server != types.DefaultUserServer {
		return jid
	}

	if lid, err := a.client.Store.LIDs.GetLIDForPN(ctx, jid); err == nil && !lid.IsEmpty() {
		return lid
	}

	return jid
}
