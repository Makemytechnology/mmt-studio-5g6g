// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package mcdata — MCData Short Data Service (SDS) + File
// Distribution (FD) + Message Store.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 23.282 §6     MCData functional model (on-network and
//                       off-network operation).
//   - TS 23.282 §7.4   Short Data Service (SDS) procedures —
//                       message structure, one-to-one and group
//                       SDS information flows. The SendPrivate /
//                       SendGroup helpers below realise the
//                       application-plane SDS data path.
//   - TS 23.282 §7.5   File Distribution (FD) procedures — upload,
//                       distribute, and accept/reject flows.
//   - TS 23.282 §7.8   Conversation management.
//   - TS 23.282 §7.13  Operations on MCData message store —
//                       server-side conversation storage, search,
//                       and replay (analogue of the
//                       GetConversation / MarkDelivered helpers
//                       below).
//
// Stage-3 protocol details (the SDS payload format on the wire,
// the MIME/MSRP carrier negotiation, exact XML body shapes) live
// in TS 24.282 which is not yet in-tree. Every TODO that needs
// stage-3 cites that TS by number.
package mcdata

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("mcx.mcdata")

// ── SDS Handler (TS 23.282 §7.4 — Short Data Service) ──
//
// SendPrivateMessage / SendGroupMessage land an SDS payload in the
// message store and mark it pending. The on-the-wire delivery
// (which carries the message via either the SIP MESSAGE method or
// MSRP per §7.4.2.x — see TS 24.282 for the stage-3 selector) is
// not yet emitted — see TODO below.
//
// TODO(spec: TS 23.282 §7.4.2.2 / §7.4.2.3): emit the SDS payload
// via the §7.4.2.2 signalling-control-plane carrier (SIP MESSAGE
// for short payloads) or the §7.4.2.3 media-plane carrier (MSRP
// for longer ones). Today we only persist the message; downstream
// subscribers poll the message store.
//
// TODO(spec: TS 24.282): the actual stage-3 SDS payload format
// (XML body + content-type) is defined in TS 24.282 which is not
// yet in-tree.

// SendPrivateMessage sends a private text message.
func SendPrivateMessage(sender, recipient, content string) map[string]interface{} {
	msgID := fmt.Sprintf("msg-%d", time.Now().UnixNano())
	engine.Exec(`INSERT INTO mcx_messages (message_id, sender, recipient, msg_type, content, delivered, created_at)
		VALUES (?,?,?,'sds',?,0,?)`, msgID, sender, recipient, content, float64(time.Now().Unix()))
	log.Infof("SDS private: %s -> %s", sender, recipient)
	return map[string]interface{}{"message_id": msgID, "sender": sender, "recipient": recipient, "content": content}
}

// SendGroupMessage sends a group text message.
func SendGroupMessage(sender string, groupID int, content string) map[string]interface{} {
	msgID := fmt.Sprintf("msg-%d", time.Now().UnixNano())
	engine.Exec(`INSERT INTO mcx_messages (message_id, sender, group_id, msg_type, content, delivered, created_at)
		VALUES (?,?,?,'sds',?,0,?)`, msgID, sender, groupID, content, float64(time.Now().Unix()))
	log.Infof("SDS group: %s -> group %d", sender, groupID)
	return map[string]interface{}{"message_id": msgID, "sender": sender, "group_id": groupID, "content": content}
}

// ── File Distribution (TS 23.282 §7.5) ──
//
// UploadFile lands the file content under FileUploadDir and
// records the metadata in the message store, mirroring the §7.5.2
// "File distribution upload" information flow. The actual download
// retrieval URL exposed to recipients is the responsibility of the
// REST handler that wraps GetFilePath().
//
// TODO(spec: TS 23.282 §7.5.2.x): the §7.5.2.x distribution and
// accept/reject information flows are not modelled — once a file
// is uploaded, a recipient polls/downloads but cannot reject.
//
// TODO(spec: TS 23.282 §7.5.3): file-distribution media-plane
// option (HTTP file upload over MSRP / FT-HTTP) is not honored;
// today we always use the local filesystem store.

const FileUploadDir = "/tmp/mcx_files"

// UploadFile stores and records a file.
func UploadFile(sender string, data []byte, filename string, recipient *string, groupID *int) map[string]interface{} {
	os.MkdirAll(FileUploadDir, 0755)
	fileID := fmt.Sprintf("file-%d", time.Now().UnixNano())
	safeName := filepath.Base(filename)
	filePath := filepath.Join(FileUploadDir, fileID+"_"+safeName)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		log.Warnf("FD upload failed: %v", err)
		return nil
	}
	msgID := fmt.Sprintf("msg-%d", time.Now().UnixNano())
	engine.Exec(`INSERT INTO mcx_messages (message_id, sender, recipient, group_id, msg_type, file_name, file_size, file_path, delivered, created_at)
		VALUES (?,?,?,?,'file',?,?,?,0,?)`, msgID, sender, recipient, groupID, safeName, len(data), filePath, float64(time.Now().Unix()))
	log.Infof("FD upload: %s by %s (%d bytes)", safeName, sender, len(data))
	return map[string]interface{}{"message_id": msgID, "sender": sender, "file_name": safeName, "file_size": len(data)}
}

// ── Message Store (TS 23.282 §7.13 — Operations on MCData
//                    message store) ──
//
// The local SQLite mcx_messages table acts as the message store;
// callers retrieve history per §7.13.1 ("MCData message store
// structure") and the §7.13.3 "Manage MCData message store" flows.
//
// TODO(spec: TS 23.282 §7.13.2): authentication / authorization on
// the message-store endpoints is not enforced — any handler with
// REST access can read or write. §7.13.2 requires per-message
// access policy lookup before returning content.
//
// TODO(spec: TS 23.282 §7.13.3): Server-side search and message-
// store query expressions are not honored; callers can only filter
// by group_id / sender / limit. Tag-based and content-search modes
// described in §7.13.3 require additional indexing.

// GetConversation retrieves message history.
func GetConversation(groupID *int, sender *string, limit int) []map[string]interface{} {
	q := "SELECT message_id, sender, recipient, group_id, msg_type, content, file_name, file_size, delivered, created_at FROM mcx_messages WHERE 1=1"
	var args []interface{}
	if groupID != nil { q += " AND group_id=?"; args = append(args, *groupID) }
	if sender != nil { q += " AND sender=?"; args = append(args, *sender) }
	q += " ORDER BY id DESC LIMIT ?"
	if limit <= 0 { limit = 100 }
	args = append(args, limit)
	rows, err := engine.Query(q, args...)
	if err != nil { return nil }
	defer rows.Close()
	var out []map[string]interface{}
	for rows.Next() {
		var msgID, snd, msgType string
		var rcpt, cont, fname interface{}
		var gid, fsize interface{}
		var delivered int
		var createdAt float64
		rows.Scan(&msgID, &snd, &rcpt, &gid, &msgType, &cont, &fname, &fsize, &delivered, &createdAt)
		out = append(out, map[string]interface{}{
			"message_id": msgID, "sender": snd, "recipient": rcpt, "group_id": gid,
			"msg_type": msgType, "content": cont, "file_name": fname, "file_size": fsize,
			"delivered": delivered, "created_at": createdAt,
		})
	}
	return out
}

// GetMessageByID returns a single message by ID.
func GetMessageByID(messageID string) map[string]interface{} {
	row := engine.QueryRow(`SELECT message_id, sender, recipient, group_id, msg_type, content, file_name, file_size, delivered, created_at
		FROM mcx_messages WHERE message_id=?`, messageID)
	var msgID, snd, msgType string
	var rcpt, cont, fname interface{}
	var gid, fsize interface{}
	var delivered int; var createdAt float64
	if row.Scan(&msgID, &snd, &rcpt, &gid, &msgType, &cont, &fname, &fsize, &delivered, &createdAt) != nil { return nil }
	return map[string]interface{}{
		"message_id": msgID, "sender": snd, "recipient": rcpt, "group_id": gid,
		"msg_type": msgType, "content": cont, "file_name": fname, "file_size": fsize,
		"delivered": delivered, "created_at": createdAt,
	}
}

// GetFilePath returns the file path for a file distribution message.
func GetFilePath(messageID string) string {
	row := engine.QueryRow(`SELECT file_path FROM mcx_messages WHERE message_id=? AND msg_type='file'`, messageID)
	var path string
	if row.Scan(&path) != nil { return "" }
	return path
}

func MarkDelivered(messageID string) {
	engine.Exec(`UPDATE mcx_messages SET delivered=1 WHERE message_id=?`, messageID)
}
