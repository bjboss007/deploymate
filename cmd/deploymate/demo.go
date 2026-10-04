package main

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/habibmuhammad/deploymate/internal/auth"
	"github.com/habibmuhammad/deploymate/internal/config"
	"github.com/habibmuhammad/deploymate/internal/crypto"
	"github.com/habibmuhammad/deploymate/internal/demo"
	"github.com/habibmuhammad/deploymate/internal/store"
)

// seedDemo fills a FRESH data directory with the fictional fleet used by the
// website's tour (internal/demo). It refuses a database that already has users,
// so it can never touch a real installation:
//
//	DEPLOYMATE_DATA_DIR=/tmp/dm-demo DEPLOYMATE_SETUP_PASSWORD=… deploymate seed-demo
func seedDemo() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	if n, err := st.CountUsers(); err != nil {
		return err
	} else if n > 0 {
		return errors.New("this data directory already has users — seed-demo only runs on an empty one")
	}
	key, err := crypto.LoadOrCreateKey(cfg.KeyPath)
	if err != nil {
		return err
	}
	password := os.Getenv("DEPLOYMATE_SETUP_PASSWORD")
	if password == "" {
		password = "demo-demo-demo"
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := demo.Seed(st, key, time.Now(), hash); err != nil {
		return err
	}
	fmt.Println("seeded demo fleet; sign in as demo@deploymate.dev")
	return nil
}
