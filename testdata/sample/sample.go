// Package sample exercises every supported logging library. It is parsed by
// tests, never compiled — imports and identifiers don't need to resolve.
package sample

func demo() {
	// stdlib log
	log.Printf("dial %s: %v", host, err)
	log.Println("starting server on", addr)
	log.Fatal("cannot open config")

	// log/slog
	slog.Info("user logged in", "user", u)
	slog.ErrorContext(ctx, "query failed", "err", err)

	// zap sugared
	sugar.Infow("cache miss", "key", key)
	sugar.Errorf("upload failed after %d retries", n)

	// zap logger
	zl.Error("db unreachable", zap.Error(err), zap.String("db", name))

	// zerolog
	zlog.Error().Str("path", p).Msg("read failed")
	zlog.Debug().Msgf("retrying in %ds", sec)

	// logrus
	logrus.WithField("id", id).Warn("slow request", took)

	// literal concat with a dynamic part
	log.Print("boot " + version)

	// fully dynamic message: counted, no template
	l.Error(err.Error())

	// no literal text: a "<*>" catch-all template would match every line
	sugar.Errorf("%v", err)

	// not logging: excluded receivers
	fmt.Errorf("not a log %s", x)
	http.Error(w, "also not a log", 500)
}
