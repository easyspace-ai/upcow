package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/betbot/gobet/pkg/secretstore"
)

func main() {
	var (
		inPath    = flag.String("in", ".env", "input .env file path")
		dbPath    = flag.String("badger", getenv("GOBET_SECRET_DB", "data/secrets.badger"), "badger secrets db path")
		secretKey = flag.String("secret-key", getenv("GOBET_SECRET_KEY", ""), "badger encryption key (32 bytes base64/hex)")
		prefix    = flag.String("prefix", "env/", "key prefix inside badger")
	)
	flag.Parse()

	keyBytes, err := secretstore.ParseKey(*secretKey)
	if err != nil {
		fatal(err)
	}
	if keyBytes == nil {
		fatal(fmt.Errorf("secret key is required: set GOBET_SECRET_KEY or pass -secret-key"))
	}

	kv, err := parseDotEnvFile(*inPath)
	if err != nil {
		fatal(err)
	}

	ss, err := secretstore.Open(secretstore.OpenOptions{
		Path:          *dbPath,
		EncryptionKey: keyBytes,
		ReadOnly:      false,
	})
	if err != nil {
		fatal(err)
	}
	defer ss.Close()

	written := 0
	for k, v := range kv {
		if err := ss.SetString((*prefix)+k, v); err != nil {
			fatal(err)
		}
		written++
	}

	fmt.Fprintf(os.Stderr, "已导入 %d 项到 badger：%s（前缀 %s）\n", written, *dbPath, *prefix)
}

func getenv(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err.Error())
	os.Exit(1)
}

func parseDotEnvFile(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		l := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if !strings.Contains(l, "=") {
			continue
		}
		parts := strings.SplitN(l, "=", 2)
		k := strings.TrimSpace(parts[0])
		v := strings.TrimSpace(parts[1])
		if k == "" {
			continue
		}
		// strip optional quotes
		if len(v) >= 2 && ((v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'')) {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out, nil
}
