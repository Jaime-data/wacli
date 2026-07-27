package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/openclaw/wacli/internal/out"
	"github.com/spf13/cobra"
	"go.mau.fi/whatsmeow/types"
)

// groupsParticipantsListPayload is the read-only projection of a group roster.
//
// `selfJid` travels with the roster on purpose. Every consumer that wants to ask
// "have all the OTHERS confirmed?" has to remove the linked device's own JID
// first, and making each of them rediscover it from somewhere else is how they
// end up off by one — a census that is short or long by one member is worse than
// no census, because it looks authoritative.
type groupsParticipantsListPayload struct {
	GroupJID     string                           `json:"groupJid"`
	SelfJID      string                           `json:"selfJid"`
	Participants []groupsParticipantsListEntryDTO `json:"participants"`
}

type groupsParticipantsListEntryDTO struct {
	UserJID   string `json:"userJid"`
	Role      string `json:"role"`
	UpdatedAt string `json:"updatedAt"`
}

func newGroupsParticipantsListCmd(flags *rootFlags) *cobra.Command {
	var group string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List a group's participants (from local DB; run sync or groups refresh to populate)",
		Long: "List a group's participants from the local store.\n\n" +
			"Read-only: unlike `groups info`, this never connects to WhatsApp and never\n" +
			"writes, so it can be run against the store of a live `sync --follow` daemon\n" +
			"without competing for the write lock. The roster is therefore as fresh as the\n" +
			"last time the daemon persisted a message for the group (it refreshes the\n" +
			"participants on each one), or as the last explicit `groups refresh`.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(group) == "" {
				return fmt.Errorf("--jid is required")
			}
			gjid, err := types.ParseJID(strings.TrimSpace(group))
			if err != nil {
				return err
			}
			if gjid.Server != types.GroupServer {
				return fmt.Errorf("--jid must be a group JID (…@%s)", types.GroupServer)
			}

			ctx, cancel := withTimeout(context.Background(), flags)
			defer cancel()

			a, lk, err := newApp(ctx, flags, false, false)
			if err != nil {
				return err
			}
			defer closeApp(a, lk)

			ps, err := a.DB().ListGroupParticipants(gjid.String())
			if err != nil {
				return err
			}

			// Best-effort, and deliberately not fatal: a store that has a roster but
			// no readable device row still answers the question that was asked. The
			// empty string is the honest report of "unknown", which a consumer can
			// act on; refusing the whole command would hide the roster too.
			var selfJID string
			if storeDir, err := resolveStoreDir(flags); err == nil {
				if _, linkedJID, err := readOnlyAuthStatus(storeDir); err == nil {
					selfJID = linkedJID
				}
			}

			if flags.asJSON {
				payload := groupsParticipantsListPayload{
					GroupJID:     gjid.String(),
					SelfJID:      selfJID,
					Participants: make([]groupsParticipantsListEntryDTO, 0, len(ps)),
				}
				for _, p := range ps {
					entry := groupsParticipantsListEntryDTO{UserJID: p.UserJID, Role: p.Role}
					if !p.UpdatedAt.IsZero() {
						entry.UpdatedAt = p.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z07:00")
					}
					payload.Participants = append(payload.Participants, entry)
				}
				return out.WriteJSON(os.Stdout, payload)
			}

			w := newTableWriter(os.Stdout)
			fmt.Fprintln(w, "USER\tROLE\tSELF")
			for _, p := range ps {
				self := "-"
				if selfJID != "" && p.UserJID == selfJID {
					self = "yes"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", p.UserJID, p.Role, self)
			}
			_ = w.Flush()
			if len(ps) == 0 {
				fmt.Fprintln(os.Stdout, "No participants known locally for this group.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&group, "jid", "", "group JID (…@g.us)")
	return cmd
}
