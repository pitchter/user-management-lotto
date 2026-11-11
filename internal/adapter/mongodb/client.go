// Package mongodb is the driven adapter that implements the
// domain.UserRepository port with the official MongoDB driver
// (go.mongodb.org/mongo-driver/v2).
//
// Everything MongoDB-specific is confined to this package: the domain and
// application layers never see a driver type (enforced by depguard).
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// UsersCollection is the name of the collection holding users.
const UsersCollection = "users"

// Config carries what is needed to connect.
//
// It is its own struct rather than the whole config.Config so the adapter is
// not tied to the application's configuration shape, which makes it easier
// to test and to reuse.
type Config struct {
	URI      string
	Database string
	Timeout  time.Duration
}

// Client wraps mongo.Client together with the selected database.
type Client struct {
	raw *mongo.Client
	db  *mongo.Database
}

// Connect dials MongoDB and pings it to prove the connection really works.
//
// In driver v2 mongo.Connect takes no context; the deadline for every
// operation that does not set its own comes from options.SetTimeout.
func Connect(ctx context.Context, cfg Config) (*Client, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}

	opts := options.Client().
		ApplyURI(cfg.URI).
		SetTimeout(cfg.Timeout).
		SetServerSelectionTimeout(cfg.Timeout).
		SetAppName("user-management-api")

	raw, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("could not connect to MongoDB: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	if err := raw.Ping(pingCtx, readpref.Primary()); err != nil {
		// Close the half-open connection before returning so nothing leaks.
		_ = raw.Disconnect(context.WithoutCancel(pingCtx))
		return nil, fmt.Errorf("could not ping MongoDB (check MONGO_URI and that MongoDB is running): %w", err)
	}

	return &Client{raw: raw, db: raw.Database(cfg.Database)}, nil
}

// Database returns the handle of the selected database.
func (c *Client) Database() *mongo.Database { return c.db }

// Ping checks that the database still answers; used by the readiness probe.
func (c *Client) Ping(ctx context.Context) error {
	return c.raw.Ping(ctx, readpref.Primary())
}

// Close shuts down the whole connection pool.
func (c *Client) Close(ctx context.Context) error {
	if c == nil || c.raw == nil {
		return nil
	}
	return c.raw.Disconnect(ctx)
}

// EnsureIndexes creates the indexes the system needs. It is idempotent, so it
// is safe to call on every start-up.
//
// The unique index on email is the database-level guarantee that no address is
// registered twice, even when two requests sign up with the same one at the
// same instant. Checking in code alone cannot close the gap between "looked
// and saw nothing" and "wrote the record".
func (c *Client) EnsureIndexes(ctx context.Context) error {
	models := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: fieldEmail, Value: 1}},
			Options: options.Index().SetUnique(true).SetName("uniq_email"),
		},
		{
			// Supports ordering by sign-up date.
			Keys:    bson.D{{Key: fieldCreatedAt, Value: -1}},
			Options: options.Index().SetName("idx_created_at_desc"),
		},
	}

	if _, err := c.db.Collection(UsersCollection).Indexes().CreateMany(ctx, models); err != nil {
		return fmt.Errorf("could not create the indexes on collection %s: %w", UsersCollection, err)
	}
	return nil
}

// isDuplicateKey reports whether an error comes from hitting a unique index.
func isDuplicateKey(err error) bool {
	return mongo.IsDuplicateKeyError(err)
}

// isNoDocuments reports whether a query matched no document.
func isNoDocuments(err error) bool {
	return errors.Is(err, mongo.ErrNoDocuments)
}
