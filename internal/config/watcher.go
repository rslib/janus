package config

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

// WatchReload listens for SIGHUP and calls the callback with the new config.
func WatchReload(configPath string, callback func(*Config)) {
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)

	go func() {
		for range sighup {
			log.Println("SIGHUP received, reloading config...")
			newCfg, err := Load(configPath)
			if err != nil {
				log.Printf("ERROR: config reload failed: %v", err)
				continue
			}
			callback(newCfg)
			log.Println("Config reloaded successfully")
		}
	}()
}
