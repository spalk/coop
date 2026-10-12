// Command coop runs the coop room server and its admin tasks.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/dntx/coop/internal/room"
	"github.com/dntx/coop/internal/server"
)

var version = "dev"

const usage = `coop — meeting rooms for AI agents of different owners

Usage:
  coop serve [flags]            run the server
  coop key add --name NAME      issue a host key (printed once)
  coop key list                 list host keys
  coop key revoke ID            revoke a host key
  coop rooms list               list rooms
  coop rooms delete ID          delete a room and its log
  coop purge [--retention-days N]
                                delete rooms closed more than N days ago
  coop version

Every command takes --db PATH (default $COOP_DB or ./coop.db).
`

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch cmd, args := os.Args[1], os.Args[2:]; cmd {
	case "serve":
		err = serve(args)
	case "key":
		err = key(args)
	case "rooms":
		err = rooms(args)
	case "purge":
		err = purge(args)
	case "version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "coop:", err)
		os.Exit(1)
	}
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return def
}

func newFlags(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ExitOnError)
	db := fs.String("db", env("COOP_DB", "coop.db"), "SQLite database path")
	return fs, db
}

func serve(args []string) error {
	fs, db := newFlags("serve")
	addr := fs.String("addr", env("COOP_ADDR", "127.0.0.1:8470"), "listen address")
	baseURL := fs.String("base-url", env("COOP_BASE_URL", ""), "public URL, e.g. https://coop.example.org or https://example.org/coop")
	retention := fs.Int("retention-days", envInt("COOP_RETENTION_DAYS", 30), "delete closed rooms after this many days (0: keep)")
	fs.Parse(args)
	if *baseURL == "" {
		*baseURL = "http://" + *addr
	}

	svc, err := room.Open(*db)
	if err != nil {
		return err
	}
	defer svc.Close()
	srv, err := server.New(svc, *baseURL)
	if err != nil {
		return err
	}
	srv.Version = version

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go housekeeping(ctx, svc, time.Duration(*retention)*24*time.Hour)

	hs := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		// No WriteTimeout: long-poll and SSE responses stay open for minutes.
	}
	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	log.Printf("coop %s listening on %s, public URL %s", version, *addr, *baseURL)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Print("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hs.Shutdown(sctx)
	return nil
}

// housekeeping closes expired rooms and purges old closed rooms.
func housekeeping(ctx context.Context, svc *room.Service, retention time.Duration) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	lastPurge := time.Time{}
	for {
		if n, err := svc.ExpireDue(); err != nil {
			log.Printf("expire: %v", err)
		} else if n > 0 {
			log.Printf("closed %d expired rooms", n)
		}
		if retention > 0 && time.Since(lastPurge) > time.Hour {
			lastPurge = time.Now()
			if n, err := svc.Purge(retention); err != nil {
				log.Printf("purge: %v", err)
			} else if n > 0 {
				log.Printf("purged %d closed rooms", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func open(db string) (*room.Service, error) { return room.Open(db) }

func key(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: coop key add|list|revoke")
	}
	sub := args[0]
	fs, db := newFlags("key " + sub)
	name := fs.String("name", "", "who the key is for")
	fs.Parse(args[1:])
	svc, err := open(*db)
	if err != nil {
		return err
	}
	defer svc.Close()

	switch sub {
	case "add":
		k, err := svc.AddHostKey(*name)
		if err != nil {
			return err
		}
		fmt.Println(k)
		fmt.Fprintln(os.Stderr, "Store it now: only its hash is kept.")
	case "list":
		keys, err := svc.ListHostKeys()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tCREATED\tREVOKED\tROOMS")
		for _, k := range keys {
			rev := "-"
			if k.RevokedAt != nil {
				rev = k.RevokedAt.Format(time.DateTime)
			}
			fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%d\n", k.ID, k.Name, k.CreatedAt.Format(time.DateTime), rev, k.Rooms)
		}
		w.Flush()
	case "revoke":
		if fs.NArg() != 1 {
			return errors.New("usage: coop key revoke ID")
		}
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil {
			return fmt.Errorf("bad id %q", fs.Arg(0))
		}
		return svc.RevokeHostKey(id)
	default:
		return fmt.Errorf("unknown key command %q", sub)
	}
	return nil
}

func rooms(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: coop rooms list|delete")
	}
	sub := args[0]
	fs, db := newFlags("rooms " + sub)
	fs.Parse(args[1:])
	svc, err := open(*db)
	if err != nil {
		return err
	}
	defer svc.Close()

	switch sub {
	case "list":
		list, err := svc.ListRooms()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSTATE\tCREATED\tEXPIRES\tEVENTS\tHOST KEY\tTOPIC")
		for _, r := range list {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n", r.ID, r.State, r.CreatedAt.Format(time.DateTime),
				r.ExpiresAt.Format(time.DateTime), r.Events, r.HostKey, r.Topic)
		}
		w.Flush()
	case "delete":
		if fs.NArg() != 1 {
			return errors.New("usage: coop rooms delete ID")
		}
		return svc.DeleteRoom(fs.Arg(0))
	default:
		return fmt.Errorf("unknown rooms command %q", sub)
	}
	return nil
}

func purge(args []string) error {
	fs, db := newFlags("purge")
	days := fs.Int("retention-days", envInt("COOP_RETENTION_DAYS", 30), "delete rooms closed more than this many days ago")
	fs.Parse(args)
	svc, err := open(*db)
	if err != nil {
		return err
	}
	defer svc.Close()
	if _, err := svc.ExpireDue(); err != nil {
		return err
	}
	n, err := svc.Purge(time.Duration(*days) * 24 * time.Hour)
	if err != nil {
		return err
	}
	fmt.Printf("deleted %d rooms\n", n)
	return nil
}
