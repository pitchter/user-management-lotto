package mongodb

import (
	"context"
	"errors"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/pitchter/7-solution/internal/domain"
)

// UserRepository stores users in MongoDB.
type UserRepository struct {
	coll *mongo.Collection
}

var _ domain.UserRepository = (*UserRepository)(nil)

// NewUserRepository builds a repository from a database handle.
func NewUserRepository(db *mongo.Database) *UserRepository {
	return &UserRepository{coll: db.Collection(UsersCollection)}
}

// Create inserts a new user and copies the ID MongoDB generated back into the
// entity.
//
// A duplicate email surfaces as domain.ErrEmailTaken, coming from the unique
// index, which is the only way to get this right under concurrent requests.
func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	if u == nil {
		return errors.New("mongodb: Create was given a nil user")
	}

	doc := newUserDoc(u)
	res, err := r.coll.InsertOne(ctx, doc)
	if err != nil {
		if isDuplicateKey(err) {
			return fmt.Errorf("%w: %s", domain.ErrEmailTaken, doc.Email)
		}
		return fmt.Errorf("could not store the user: %w", err)
	}

	if oid, ok := res.InsertedID.(bson.ObjectID); ok {
		u.ID = oid.Hex()
	}
	return nil
}

// GetByID fetches a user by id.
func (r *UserRepository) GetByID(ctx context.Context, id string) (*domain.User, error) {
	oid, err := parseID(id)
	if err != nil {
		return nil, err
	}

	var doc userDoc
	if err := r.coll.FindOne(ctx, bson.D{{Key: fieldID, Value: oid}}).Decode(&doc); err != nil {
		if isNoDocuments(err) {
			return nil, fmt.Errorf("%w (id %s)", domain.ErrUserNotFound, id)
		}
		return nil, fmt.Errorf("could not look the user up by id: %w", err)
	}

	u := doc.toDomain()
	return &u, nil
}

// GetByEmail fetches a user by email, always normalising it first so it
// matches the stored form.
func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	normalized := domain.NormalizeEmail(email)

	var doc userDoc
	err := r.coll.FindOne(ctx, bson.D{{Key: fieldEmail, Value: normalized}}).Decode(&doc)
	if err != nil {
		if isNoDocuments(err) {
			return nil, fmt.Errorf("%w (email %s)", domain.ErrUserNotFound, normalized)
		}
		return nil, fmt.Errorf("could not look the user up by email: %w", err)
	}

	u := doc.toDomain()
	return &u, nil
}

// List returns a page of users using keyset pagination.
//
// It filters on _id > cursor and sorts by _id ascending, rather than using
// skip/offset, because:
//
//   - skip makes MongoDB walk past every skipped document, so deep pages get
//     slower and slower, while _id > cursor rides the index directly and
//     costs the same for every page;
//
//   - an offset shifts the window when someone signs up mid-paging, which
//     duplicates or drops records, whereas a cursor pins the position.
//
// One extra record is fetched to learn whether another page exists without a
// second query.
func (r *UserRepository) List(ctx context.Context, p domain.ListParams) (domain.ListResult, error) {
	p = p.Normalize()

	filter := bson.D{}
	if p.Cursor != "" {
		oid, err := bson.ObjectIDFromHex(p.Cursor)
		if err != nil {
			return domain.ListResult{}, domain.NewValidationError("cursor", "must be a valid cursor returned by a previous page")
		}
		filter = bson.D{{Key: fieldID, Value: bson.D{{Key: "$gt", Value: oid}}}}
	}

	opts := options.Find().
		SetSort(bson.D{{Key: fieldID, Value: 1}}).
		SetLimit(int64(p.Limit) + 1)

	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return domain.ListResult{}, fmt.Errorf("could not list users: %w", err)
	}
	defer func() { _ = cur.Close(ctx) }()

	var docs []userDoc
	if decodeErr := cur.All(ctx, &docs); decodeErr != nil {
		return domain.ListResult{}, fmt.Errorf("could not decode the user list: %w", decodeErr)
	}

	var nextCursor string
	if len(docs) > p.Limit {
		// The extra record is only a signal that more exist; it is not returned.
		docs = docs[:p.Limit]
		nextCursor = docs[len(docs)-1].ID.Hex()
	}

	users := make([]domain.User, 0, len(docs))
	for _, d := range docs {
		users = append(users, d.toDomain())
	}

	total, err := r.Count(ctx)
	if err != nil {
		return domain.ListResult{}, err
	}

	return domain.ListResult{Users: users, NextCursor: nextCursor, Total: total}, nil
}

// Update persists changes to a user.
//
// Only the mutable fields are written (name, email, password_hash, role,
// updated_at) with $set, rather than replacing the whole document, so _id
// and created_at are never touched and no future field is silently dropped.
func (r *UserRepository) Update(ctx context.Context, u *domain.User) error {
	if u == nil {
		return errors.New("mongodb: Update was given a nil user")
	}

	oid, err := parseID(u.ID)
	if err != nil {
		return err
	}

	update := bson.D{{Key: "$set", Value: bson.D{
		{Key: fieldName, Value: u.Name},
		{Key: fieldEmail, Value: domain.NormalizeEmail(u.Email)},
		{Key: fieldPasswordHash, Value: u.PasswordHash},
		{Key: fieldRole, Value: u.Role.String()},
		{Key: fieldUpdatedAt, Value: u.UpdatedAt.UTC()},
	}}}

	res, err := r.coll.UpdateOne(ctx, bson.D{{Key: fieldID, Value: oid}}, update)
	if err != nil {
		if isDuplicateKey(err) {
			return fmt.Errorf("%w: %s", domain.ErrEmailTaken, domain.NormalizeEmail(u.Email))
		}
		return fmt.Errorf("could not update the user: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("%w (id %s)", domain.ErrUserNotFound, u.ID)
	}
	return nil
}

// Delete removes a user by id.
func (r *UserRepository) Delete(ctx context.Context, id string) error {
	oid, err := parseID(id)
	if err != nil {
		return err
	}

	res, err := r.coll.DeleteOne(ctx, bson.D{{Key: fieldID, Value: oid}})
	if err != nil {
		return fmt.Errorf("could not delete the user: %w", err)
	}
	if res.DeletedCount == 0 {
		return fmt.Errorf("%w (id %s)", domain.ErrUserNotFound, id)
	}
	return nil
}

// Count returns the total number of users (used by the background worker and List).
//
// It uses CountDocuments, which really counts through the index, instead of
// EstimatedDocumentCount, which reads collection metadata and can drift.
func (r *UserRepository) Count(ctx context.Context) (int64, error) {
	n, err := r.coll.CountDocuments(ctx, bson.D{})
	if err != nil {
		return 0, fmt.Errorf("could not count the users: %w", err)
	}
	return n, nil
}
