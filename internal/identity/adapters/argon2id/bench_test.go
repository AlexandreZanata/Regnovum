package argon2id_test

import (
	"testing"

	"github.com/AlexandreZanata/Regnovum/internal/identity/adapters/argon2id"
	"github.com/AlexandreZanata/Regnovum/internal/platform/clockseed"
)

func BenchmarkHashPassword_Default(b *testing.B) {
	hasher, err := argon2id.New(argon2id.DefaultParams(), clockseed.NewRandom())
	if err != nil {
		b.Fatalf("failed to create hasher: %v", err)
	}
	password := "BenchmarkPassword2026!"

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := hasher.HashPassword(password)
		if err != nil {
			b.Fatalf("HashPassword failed: %v", err)
		}
	}
}

func BenchmarkVerifyPassword_Default(b *testing.B) {
	hasher, err := argon2id.New(argon2id.DefaultParams(), clockseed.NewRandom())
	if err != nil {
		b.Fatalf("failed to create hasher: %v", err)
	}
	password := "BenchmarkPassword2026!"
	encodedHash, err := hasher.HashPassword(password)
	if err != nil {
		b.Fatalf("failed to hash password: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		match, err := hasher.VerifyPassword(password, encodedHash)
		if err != nil || !match {
			b.Fatalf("VerifyPassword failed: match=%v, err=%v", match, err)
		}
	}
}

func BenchmarkVerifyDummy_Default(b *testing.B) {
	hasher, err := argon2id.New(argon2id.DefaultParams(), clockseed.NewRandom())
	if err != nil {
		b.Fatalf("failed to create hasher: %v", err)
	}
	dummy := hasher.DummyHash()
	candidatePassword := "CandidateAttempt123!"

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		match, err := hasher.VerifyPassword(candidatePassword, dummy)
		if err != nil || match {
			b.Fatalf("VerifyPassword dummy unexpected: match=%v, err=%v", match, err)
		}
	}
}

func BenchmarkHashPassword_Fast(b *testing.B) {
	hasher, err := argon2id.New(argon2id.FastParams(), clockseed.NewRandom())
	if err != nil {
		b.Fatalf("failed to create hasher: %v", err)
	}
	password := "BenchmarkPassword2026!"

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := hasher.HashPassword(password)
		if err != nil {
			b.Fatalf("HashPassword failed: %v", err)
		}
	}
}

func BenchmarkVerifyPassword_Fast(b *testing.B) {
	hasher, err := argon2id.New(argon2id.FastParams(), clockseed.NewRandom())
	if err != nil {
		b.Fatalf("failed to create hasher: %v", err)
	}
	password := "BenchmarkPassword2026!"
	encodedHash, err := hasher.HashPassword(password)
	if err != nil {
		b.Fatalf("failed to hash password: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		match, err := hasher.VerifyPassword(password, encodedHash)
		if err != nil || !match {
			b.Fatalf("VerifyPassword failed: match=%v, err=%v", match, err)
		}
	}
}
