package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ardwiinoo/tunas/internal/agent"
	"github.com/ardwiinoo/tunas/internal/config"
	"github.com/ardwiinoo/tunas/internal/secret"
)

// Where the encrypted secrets live (Windows DPAPI, this Windows account only).
func tokenStore() secret.Store {
	return secret.Store{Path: filepath.Join(appDir("APPDATA", os.UserConfigDir), "token.bin"), P: secret.Default()}
}

func clientKeyStore() secret.Store {
	return secret.Store{Path: filepath.Join(appDir("APPDATA", os.UserConfigDir), "clientkey.bin"), P: secret.Default()}
}

// setToken is --set-token: the token is typed or pasted at a prompt (echo
// off) or piped in on stdin. It is never a command-line argument, so it does
// not end up in the shell history, and it is never printed.
func setToken(cfgPath string) error {
	fmt.Println("Tempel token perangkat (tnd_...) lalu tekan Enter. Tidak akan ditampilkan.")
	tok, err := agent.ReadSecretLine()
	if err != nil {
		return errors.New("no token read from stdin")
	}
	if err := agent.StoreDeviceToken(tokenStore(), tok); err != nil {
		return err
	}
	fmt.Println("Token disimpan terenkripsi di", tokenStore().Path, "(hanya akun Windows ini yang bisa membukanya).")
	// a plain copy left in config.yaml is removed too (the line may be absent: fine)
	if err := config.ScrubDeviceToken(cfgPath); err == nil {
		fmt.Println("Baris device_token lama di config.yaml dihapus.")
	}
	fmt.Println("Mulai ulang agen supaya memakai token baru.")
	return nil
}

// setClientKey is --set-client-key <file>: the mTLS private key (PEM) goes
// into the encrypted store, so the plain file can be deleted afterwards.
func setClientKey(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot read %s: %w", path, err)
	}
	if err := agent.StoreClientKey(clientKeyStore(), b); err != nil {
		return err
	}
	fmt.Println("Kunci klien disimpan terenkripsi di", clientKeyStore().Path+".")
	fmt.Println("Hapus berkas kunci biasa sekarang (", path, ") lalu kosongkan tls.client_key di config.yaml.")
	return nil
}

// pressEnter keeps an own console window open until it is read.
func pressEnter() {
	fmt.Println("Tekan Enter untuk menutup...")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
