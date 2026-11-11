package mongodb

import (
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"user-management-api/internal/domain"
)

// Document field names, as constants so no query can misspell one.
const (
	fieldID           = "_id"
	fieldName         = "name"
	fieldEmail        = "email"
	fieldPasswordHash = "password_hash"
	fieldRole         = "role"
	fieldCreatedAt    = "created_at"
	fieldUpdatedAt    = "updated_at"
)

// userDoc is the shape of a document in the users collection.
//
// Keeping it separate from domain.User is deliberate: database field names
// can change, and storage-only fields can be added, without touching the
// entity — and the domain stays free of bson tags, which would amount to
// the core knowing about MongoDB.
type userDoc struct {
	ID           bson.ObjectID `bson:"_id,omitempty"`
	Name         string        `bson:"name"`
	Email        string        `bson:"email"`
	PasswordHash string        `bson:"password_hash"`
	Role         string        `bson:"role"`
	CreatedAt    time.Time     `bson:"created_at"`
	UpdatedAt    time.Time     `bson:"updated_at"`
}

// toDomain converts a document into an entity.
func (d userDoc) toDomain() domain.User {
	return domain.User{
		ID:           d.ID.Hex(),
		Name:         d.Name,
		Email:        d.Email,
		PasswordHash: d.PasswordHash,
		Role:         domain.Role(d.Role),
		// MongoDB stores times as UTC milliseconds. Pinning the location to UTC
		// here keeps the result independent of the host's time zone.
		CreatedAt: d.CreatedAt.UTC(),
		UpdatedAt: d.UpdatedAt.UTC(),
	}
}

// newUserDoc converts an entity into a document for insertion, leaving the
// _id for MongoDB to generate.
func newUserDoc(u *domain.User) userDoc {
	return userDoc{
		Name:         u.Name,
		Email:        domain.NormalizeEmail(u.Email),
		PasswordHash: u.PasswordHash,
		Role:         u.Role.String(),
		CreatedAt:    u.CreatedAt.UTC(),
		UpdatedAt:    u.UpdatedAt.UTC(),
	}
}

// parseID converts a hex-string id into an ObjectID.
//
// A malformed id can never match any document, so it is reported as
// ErrUserNotFound (a 404) rather than a system error. That stops a stream of
// junk ids from turning into 500s.
func parseID(id string) (bson.ObjectID, error) {
	oid, err := bson.ObjectIDFromHex(id)
	if err != nil {
		return bson.NilObjectID, fmt.Errorf("%w (id %q is not a valid identifier)", domain.ErrUserNotFound, id)
	}
	return oid, nil
}
