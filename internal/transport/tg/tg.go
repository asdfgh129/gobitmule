// Package tgtransport implements the Telegram file transport (WBS 15 /
// FR-SRC-04, ADR-0029): MTProto file download through gotd/td with the
// scheduler's sequential-commit contract (tputil.SequentialCommit), FLOOD_WAIT
// classification into the unified error model and session storage via the
// platform credential store (FR-SRC-06 — the MTProto session blob lives in
// the OS keychain, never in a plaintext file).
//
// L1 boundaries (honest, ADR-0029):
//   - public channels/users only: private `t.me/c/<id>/<msg>` links need the
//     account's channel access hash (dialogs sync) and are refused;
//   - sessions are READ-ONLY in L1: the blob is loaded from the credential
//     store per run; auth migrations rotate the session in memory only and a
//     new blob must be stored to persist it;
//   - REAL MTProto interoperability is NOT_EXECUTED: the production client is
//     compiled and wired but a live Telegram DC session requires credentials
//     and real network (`12` §1 forbids it in CI). The seam-injected tests
//     cover everything except the wire protocol.
package tgtransport

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"

	"github.com/asdfgh129/gobitmule/internal/core"
	"github.com/asdfgh129/gobitmule/internal/transport/tputil"
)

// MediaClient is the MTProto seam. The production implementation drives
// gotd/td; tests inject a fake (docs/12 §1: CI never touches real Telegram).
type MediaClient interface {
	// Stat resolves one message reference to its document metadata.
	Stat(ctx context.Context, ref LinkRef) (size int64, fileName string, err error)
	// Stream downloads the document bytes into w.
	Stream(ctx context.Context, ref LinkRef, w io.Writer) error
}

// Transport is the Telegram implementation of core.Transport.
type Transport struct {
	creds   core.CredentialStore
	apiID   int
	apiHash string
	timeout time.Duration
	// media is the seam; nil binds the production gotd client.
	media MediaClient
}

// New builds the transport. apiID/apiHash are the Telegram APP credentials
// (docs/11 §3: the USER session blob goes through the credential store).
func New(creds core.CredentialStore, apiID int, apiHash string, timeout time.Duration) *Transport {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	return &Transport{creds: creds, apiID: apiID, apiHash: apiHash, timeout: timeout}
}

// newTransportWithMedia is the offline-test seam.
func newTransportWithMedia(creds core.CredentialStore, media MediaClient) *Transport {
	return &Transport{creds: creds, media: media, timeout: time.Minute}
}

func (t *Transport) Name() string { return "telegram" }

// Supports claims resources the tg resolver produced: the mirror URL carries
// the internal tg://ud scheme and the identity records the variant.
func (t *Transport) Supports(r *core.Resource) bool {
	if r == nil || len(r.Mirrors) == 0 {
		return false
	}
	return strings.HasPrefix(strings.ToLower(r.Mirrors[0].URL), "tg://ud/")
}

// mediaFor returns the client for one task: the seam in tests, the production
// gotd client (bound to the task's session credential) otherwise.
func (t *Transport) mediaFor(r *core.Resource) (MediaClient, error) {
	if t.media != nil {
		return t.media, nil
	}
	if r.Credential == "" {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"telegram download needs an MTProto session: store one via POST /credentials and pass its credentialRef with the task (docs/07 §6)")
	}
	if t.apiID <= 0 || t.apiHash == "" {
		return nil, core.Err(core.E_CAPABILITY_UNAVAILABLE,
			"telegram is not configured: set sources.telegram.apiId/apiHash (ADR-0029)")
	}
	return &gotdMedia{apiID: t.apiID, apiHash: t.apiHash, creds: t.creds,
		credentialRef: r.Credential}, nil
}

// classifyTGError maps MTProto errors into the unified error model
// (ADR-0009: FLOOD_WAIT belongs in KindTransient with the server-given wait).
func classifyTGError(err error) error {
	if err == nil {
		return nil
	}
	if d, ok := tgerr.AsFloodWait(err); ok {
		return core.Err(core.E_TRANSIENT_NETWORK,
			"FLOOD_WAIT: telegram asked to retry after %s", d)
	}
	return err
}

func (t *Transport) Probe(ctx context.Context, r *core.Resource) (*core.ProbeResult, error) {
	if !t.Supports(r) {
		return nil, core.Err(core.E_RESOLVE_FAILED, "telegram transport requires a tg resource")
	}
	ref, err := ParseNormalized(r.Mirrors[0].URL)
	if err != nil {
		return nil, err
	}
	media, err := t.mediaFor(r)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	size, fileName, err := media.Stat(cctx, ref)
	if err != nil {
		return nil, classifyTGError(err)
	}
	if size <= 0 {
		return nil, core.Err(core.E_RESOLVE_FAILED, "telegram document has no size")
	}
	if fileName == "" {
		fileName = fmt.Sprintf("tg-%s-%d", ref.Channel, ref.MsgID)
	}
	return &core.ProbeResult{Size: size, AcceptRanges: true, Filename: fileName}, nil
}

// Run streams the document through the scheduler's chunk plan. Resume works
// by skipping to the first pending chunk (bytes re-traverse the network and
// are charged to the Budget; gotd's WithOffset-based resume is a future
// optimisation).
func (t *Transport) Run(ctx context.Context, r *core.Resource, w core.ChunkWriter, b core.Budget) error {
	if !t.Supports(r) {
		return core.Err(core.E_RESOLVE_FAILED, "telegram transport requires a tg resource")
	}
	ref, err := ParseNormalized(r.Mirrors[0].URL)
	if err != nil {
		return err
	}
	media, err := t.mediaFor(r)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	size, _, err := media.Stat(cctx, ref)
	if err != nil {
		return classifyTGError(err)
	}

	first, ok, err := w.Next(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return nil // nothing pending
	}

	// gotd Stream writes into an io.Writer; a pipe bridges it to the
	// sequential-commit reader, with every byte charged to the Budget.
	pr, pw := io.Pipe()
	streamErr := make(chan error, 1)
	go func() {
		defer pw.Close()
		streamErr <- classifyTGError(media.Stream(cctx, ref, pw))
	}()
	sr := tputil.NewChanReader(func() ([]byte, error, bool) {
		buf := make([]byte, 32<<10)
		n, rerr := pr.Read(buf)
		if rerr == io.EOF {
			return nil, nil, false
		}
		if rerr != nil {
			return nil, rerr, true
		}
		return buf[:n], nil, true
	}, func() { _ = pr.Close() }, b)
	defer sr.Close()

	// Commit the download; errors here are primary.
	runErr := func() error {
		if skip := first.Start; skip > 0 {
			if _, err := io.CopyN(io.Discard, sr, skip); err != nil {
				_ = w.Release(ctx, first, err)
				return err
			}
		}
		return tputil.SequentialCommit(ctx, sr, size, 0, w, first)
	}()
	// sr.Close() aborts the pipe (the stream goroutine unblocks with
	// ErrClosedPipe); the writer-side error is received EXACTLY ONCE here —
	// a second receive deadlocked (found by the ADR-0028-era test run).
	sr.Close()
	streamSide := <-streamErr
	if runErr != nil {
		return runErr
	}
	return streamSide
}

// ---------- production gotd implementation ----------

// credSessionStorage loads the MTProto session blob from the platform
// credential store (FR-SRC-06). L1 is read-only: gotd's in-run session
// mutations stay in memory and a rotated session must be re-stored to
// persist it (ADR-0029).
type credSessionStorage struct {
	creds core.CredentialStore
	ref   core.CredentialRef
}

func (s credSessionStorage) LoadSession(ctx context.Context) ([]byte, error) {
	data, err := s.creds.Get(ctx, s.ref)
	if err != nil || data == "" {
		return nil, session.ErrNotFound
	}
	return []byte(data), nil
}

func (s credSessionStorage) StoreSession(ctx context.Context, data []byte) error {
	// Read-only L1 (ADR-0029): the loaded session is enough for downloads;
	// rotating auth keys must be persisted by storing a NEW session blob.
	return nil
}

// gotdMedia is the production MediaClient: a real gotd/td client driven
// per-operation (each Stat/Stream opens the connection, loads the session
// from the credential store and closes it — Telegram file downloads are
// bursty, and the daemon keeps no idle MTProto connection).
type gotdMedia struct {
	apiID         int
	apiHash       string
	creds         core.CredentialStore
	credentialRef core.CredentialRef
}

func (g *gotdMedia) withClient(ctx context.Context, fn func(api *tg.Client) error) error {
	mem := session.StorageMemory{}
	if data, err := g.creds.Get(ctx, g.credentialRef); err == nil && data != "" {
		_ = mem.StoreSession(ctx, []byte(data))
	}
	client := telegram.NewClient(g.apiID, g.apiHash, telegram.Options{
		SessionStorage: &mem,
		NoUpdates:      true,
	})
	return client.Run(ctx, func(ctx context.Context) error {
		// *telegram.Client implements tg.Invoker; the generated tg.Client
		// wraps it and exposes the full RPC surface (incl. downloader.Client).
		return fn(tg.NewClient(client))
	})
}

// resolveDocument resolves one link reference to its document through the
// authenticated API. Public channels/users only: a private link carries no
// channel access hash, and hashing requires the account's dialogs (P3).
func (g *gotdMedia) resolveDocument(ctx context.Context, api *tg.Client, ref LinkRef) (*tg.Document, error) {
	if ref.Private {
		return nil, ErrPrivateLinks
	}
	resolved, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
		Username: ref.Channel,
	})
	if err != nil {
		return nil, classifyTGError(err)
	}
	var inputChannel tg.InputChannelClass
	for _, chat := range resolved.Chats {
		if c, ok := chat.(*tg.Channel); ok {
			inputChannel = &tg.InputChannel{ChannelID: c.ID, AccessHash: c.AccessHash}
			break
		}
	}
	if inputChannel == nil {
		return nil, core.Err(core.E_RESOLVE_FAILED,
			"telegram: username %q resolved to no channel", ref.Channel)
	}
	msgs, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: inputChannel,
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: ref.MsgID}},
	})
	if err != nil {
		return nil, classifyTGError(err)
	}
	var messageList []tg.MessageClass
	switch mm := msgs.(type) {
	case *tg.MessagesMessages:
		messageList = mm.Messages
	case *tg.MessagesMessagesSlice:
		messageList = mm.Messages
	}
	for _, m := range messageList {
		msg, ok := m.(*tg.Message)
		if !ok || msg.ID != ref.MsgID {
			continue
		}
		media, ok := msg.Media.(*tg.MessageMediaDocument)
		if !ok {
			continue
		}
		doc, ok := media.Document.(*tg.Document)
		if !ok {
			continue
		}
		return doc, nil
	}
	return nil, core.Err(core.E_RESOLVE_FAILED,
		"telegram: message %d carries no downloadable document", ref.MsgID)
}

func documentName(doc *tg.Document) string {
	for _, a := range doc.Attributes {
		if fn, ok := a.(*tg.DocumentAttributeFilename); ok {
			return fn.FileName
		}
	}
	return ""
}

func (g *gotdMedia) Stat(ctx context.Context, ref LinkRef) (int64, string, error) {
	var size int64
	var name string
	err := g.withClient(ctx, func(api *tg.Client) error {
		doc, err := g.resolveDocument(ctx, api, ref)
		if err != nil {
			return err
		}
		size = doc.Size
		name = documentName(doc)
		return nil
	})
	return size, name, err
}

// Stream downloads the whole document (ADR-0009: WithPartSize 512KiB, CDN
// redirects enabled; FLOOD_WAIT/DC errors surface through classifyTGError).
func (g *gotdMedia) Stream(ctx context.Context, ref LinkRef, w io.Writer) error {
	return g.withClient(ctx, func(api *tg.Client) error {
		doc, err := g.resolveDocument(ctx, api, ref)
		if err != nil {
			return err
		}
		loc := &tg.InputDocumentFileLocation{
			ID:            doc.ID,
			AccessHash:    doc.AccessHash,
			FileReference: doc.FileReference,
			ThumbSize:     "",
		}
		dl := downloader.NewDownloader().WithPartSize(512 << 10).WithAllowCDN(true)
		_, err = dl.Download(api, loc).WithSize(doc.Size).Stream(ctx, w)
		return err
	})
}
