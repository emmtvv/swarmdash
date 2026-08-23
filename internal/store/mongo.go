// Package store persists admin-side state (users, sessions, audit log,
// tokens, ...). MongoStore, below, is the multi-replica backend: every
// admin replica points at the same MongoDB deployment (standalone, replica
// set, or sharded cluster - the driver treats them the same), so running
// more than one replica for HA is just a matter of raising deploy.replicas.
// See sqlite.go for the single-replica, no-external-dependency alternative.
package store

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const opTimeout = 10 * time.Second

type MongoStore struct {
	client *mongo.Client
	db     *mongo.Database
}

// Config for the collections used below:
//   - users, sessions, api_tokens, registry_credentials, webhooks,
//     gitops_stacks, deploy_hooks are keyed by their natural string ID
//     (_id), so Put is a straight upsert.
//   - audit_log, task_events, cluster_samples are append-only logs; each
//     gets an auto-incrementing "seq" (via the counters collection) so
//     ordering/pagination matches the old bbolt-backed behavior.
func OpenMongo(ctx context.Context, uri, database string) (*MongoStore, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("connect to mongo: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	if err := client.Ping(pingCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ping mongo: %w", err)
	}
	ms := &MongoStore{client: client, db: client.Database(database)}
	if err := ms.ensureIndexes(ctx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("ensure mongo indexes: %w", err)
	}
	return ms, nil
}

func (m *MongoStore) ensureIndexes(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	// Sessions expire on their own; Mongo's TTL monitor sweeps them out
	// instead of admin having to prune expired ones itself.
	_, err := m.col("sessions").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "expires_at", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(0),
	})
	if err != nil {
		return err
	}

	// Failed-login rows are pruned a day after the account's last failure -
	// well past both the attempt window and the lockout duration
	// (loginAttemptWindow/loginLockDuration in internal/admin/auth.go), so
	// this is pure hygiene rather than something the lockout logic depends
	// on.
	if _, err := m.col("login_attempts").Indexes().CreateOne(ctx, mongo.IndexModel{
		Keys:    bson.D{{Key: "last_failure", Value: 1}},
		Options: options.Index().SetExpireAfterSeconds(24 * 60 * 60),
	}); err != nil {
		return err
	}

	for _, idx := range []struct {
		collection string
		key        string
	}{
		{"api_tokens", "hash"},
		{"deploy_hooks", "hash"},
		{"deploy_hooks", "service_name"},
		{"task_events", "service_name"},
	} {
		if _, err := m.col(idx.collection).Indexes().CreateOne(ctx, mongo.IndexModel{
			Keys: bson.D{{Key: idx.key, Value: 1}},
		}); err != nil {
			return err
		}
	}
	return nil
}

func (m *MongoStore) col(name string) *mongo.Collection { return m.db.Collection(name) }

func (m *MongoStore) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	return m.client.Disconnect(ctx)
}

// nextSeq atomically increments and returns the next value of a named
// counter, using the classic Mongo find-and-modify auto-increment pattern
// (there's no native auto-increment, unlike bbolt's NextSequence/SQL's
// SERIAL) so append-only logs keep a stable insertion order across replicas.
func (m *MongoStore) nextSeq(ctx context.Context, name string) (uint64, error) {
	var result struct {
		Seq uint64 `bson:"seq"`
	}
	err := m.col("counters").FindOneAndUpdate(
		ctx,
		bson.M{"_id": name},
		bson.M{"$inc": bson.M{"seq": uint64(1)}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&result)
	return result.Seq, err
}

func ctxTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTimeout)
}

func notFound(err error) error {
	if err == mongo.ErrNoDocuments {
		return ErrNotFound
	}
	return err
}

func (m *MongoStore) PutUser(u User) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("users").ReplaceOne(ctx, bson.M{"_id": u.Username}, u, options.Replace().SetUpsert(true))
	return err
}

func (m *MongoStore) GetUser(username string) (User, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	var u User
	err := m.col("users").FindOne(ctx, bson.M{"_id": username}).Decode(&u)
	return u, notFound(err)
}

func (m *MongoStore) HasAnyUser() (bool, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	err := m.col("users").FindOne(ctx, bson.M{}).Err()
	if err == mongo.ErrNoDocuments {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (m *MongoStore) ListUsers() ([]User, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	cur, err := m.col("users").Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []User
	err = cur.All(ctx, &out)
	return out, err
}

func (m *MongoStore) DeleteUser(username string) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("users").DeleteOne(ctx, bson.M{"_id": username})
	return err
}

func (m *MongoStore) PutSession(sess Session) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("sessions").ReplaceOne(ctx, bson.M{"_id": sess.Token}, sess, options.Replace().SetUpsert(true))
	return err
}

func (m *MongoStore) GetSession(token string) (Session, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	var s Session
	err := m.col("sessions").FindOne(ctx, bson.M{"_id": token}).Decode(&s)
	return s, notFound(err)
}

func (m *MongoStore) DeleteSession(token string) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("sessions").DeleteOne(ctx, bson.M{"_id": token})
	return err
}

func (m *MongoStore) PutLoginAttempt(a LoginAttempt) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("login_attempts").ReplaceOne(ctx, bson.M{"_id": a.Username}, a, options.Replace().SetUpsert(true))
	return err
}

func (m *MongoStore) GetLoginAttempt(username string) (LoginAttempt, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	var a LoginAttempt
	err := m.col("login_attempts").FindOne(ctx, bson.M{"_id": username}).Decode(&a)
	return a, notFound(err)
}

func (m *MongoStore) DeleteLoginAttempt(username string) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("login_attempts").DeleteOne(ctx, bson.M{"_id": username})
	return err
}

func (m *MongoStore) AppendAudit(e AuditEntry) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	e.Time = time.Now()
	seq, err := m.nextSeq(ctx, "audit_log")
	if err != nil {
		return err
	}
	e.ID = seq
	_, err = m.col("audit_log").InsertOne(ctx, e)
	return err
}

func (m *MongoStore) ListAudit(skip, limit int) ([]AuditEntry, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	cur, err := m.col("audit_log").Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "seq", Value: -1}}).SetSkip(int64(skip)).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []AuditEntry
	err = cur.All(ctx, &out)
	return out, err
}

func (m *MongoStore) CountAudit() (int, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	n, err := m.col("audit_log").CountDocuments(ctx, bson.M{})
	return int(n), err
}

func (m *MongoStore) PutAPIToken(t APIToken) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("api_tokens").ReplaceOne(ctx, bson.M{"_id": t.ID}, t, options.Replace().SetUpsert(true))
	return err
}

func (m *MongoStore) ListAPITokens() ([]APIToken, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	cur, err := m.col("api_tokens").Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []APIToken
	err = cur.All(ctx, &out)
	return out, err
}

func (m *MongoStore) FindAPITokenByHash(hash string) (APIToken, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	var t APIToken
	err := m.col("api_tokens").FindOne(ctx, bson.M{"hash": hash}).Decode(&t)
	return t, notFound(err)
}

func (m *MongoStore) TouchAPIToken(id string) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("api_tokens").UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"last_used_at": time.Now()}})
	return err
}

func (m *MongoStore) DeleteAPIToken(id string) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("api_tokens").DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (m *MongoStore) PutRegistryCredential(c RegistryCredential) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("registry_credentials").ReplaceOne(ctx, bson.M{"_id": c.Server}, c, options.Replace().SetUpsert(true))
	return err
}

func (m *MongoStore) GetRegistryCredential(server string) (RegistryCredential, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	var c RegistryCredential
	err := m.col("registry_credentials").FindOne(ctx, bson.M{"_id": server}).Decode(&c)
	return c, notFound(err)
}

func (m *MongoStore) ListRegistryCredentials() ([]RegistryCredential, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	cur, err := m.col("registry_credentials").Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []RegistryCredential
	err = cur.All(ctx, &out)
	return out, err
}

func (m *MongoStore) DeleteRegistryCredential(server string) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("registry_credentials").DeleteOne(ctx, bson.M{"_id": server})
	return err
}

func (m *MongoStore) AppendTaskEvent(e TaskEvent) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	e.Time = time.Now()
	seq, err := m.nextSeq(ctx, "task_events")
	if err != nil {
		return err
	}
	e.ID = seq
	_, err = m.col("task_events").InsertOne(ctx, e)
	return err
}

func (m *MongoStore) ListTaskEvents(skip, limit int, serviceName string) ([]TaskEvent, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	filter := bson.M{}
	if serviceName != "" {
		filter["service_name"] = serviceName
	}
	cur, err := m.col("task_events").Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "seq", Value: -1}}).SetSkip(int64(skip)).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []TaskEvent
	err = cur.All(ctx, &out)
	return out, err
}

func (m *MongoStore) CountTaskEvents(serviceName string) (int, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	filter := bson.M{}
	if serviceName != "" {
		filter["service_name"] = serviceName
	}
	n, err := m.col("task_events").CountDocuments(ctx, filter)
	return int(n), err
}

func (m *MongoStore) PutWebhook(w Webhook) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("webhooks").ReplaceOne(ctx, bson.M{"_id": w.ID}, w, options.Replace().SetUpsert(true))
	return err
}

func (m *MongoStore) ListWebhooks() ([]Webhook, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	cur, err := m.col("webhooks").Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []Webhook
	err = cur.All(ctx, &out)
	return out, err
}

func (m *MongoStore) DeleteWebhook(id string) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("webhooks").DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (m *MongoStore) PutGitStack(g GitStack) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("gitops_stacks").ReplaceOne(ctx, bson.M{"_id": g.ID}, g, options.Replace().SetUpsert(true))
	return err
}

func (m *MongoStore) ListGitStacks() ([]GitStack, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	cur, err := m.col("gitops_stacks").Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []GitStack
	err = cur.All(ctx, &out)
	return out, err
}

func (m *MongoStore) GetGitStack(id string) (GitStack, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	var g GitStack
	err := m.col("gitops_stacks").FindOne(ctx, bson.M{"_id": id}).Decode(&g)
	return g, notFound(err)
}

func (m *MongoStore) DeleteGitStack(id string) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("gitops_stacks").DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (m *MongoStore) PutDeployHook(h DeployHook) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("deploy_hooks").ReplaceOne(ctx, bson.M{"_id": h.ID}, h, options.Replace().SetUpsert(true))
	return err
}

func (m *MongoStore) ListDeployHooks() ([]DeployHook, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	cur, err := m.col("deploy_hooks").Find(ctx, bson.M{})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []DeployHook
	err = cur.All(ctx, &out)
	return out, err
}

func (m *MongoStore) ListDeployHooksForService(serviceName string) ([]DeployHook, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	cur, err := m.col("deploy_hooks").Find(ctx, bson.M{"service_name": serviceName})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []DeployHook
	err = cur.All(ctx, &out)
	return out, err
}

func (m *MongoStore) FindDeployHookByHash(hash string) (DeployHook, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	var h DeployHook
	err := m.col("deploy_hooks").FindOne(ctx, bson.M{"hash": hash}).Decode(&h)
	return h, notFound(err)
}

func (m *MongoStore) TouchDeployHook(id string) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("deploy_hooks").UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"last_used_at": time.Now()}})
	return err
}

func (m *MongoStore) DeleteDeployHook(id string) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("deploy_hooks").DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (m *MongoStore) AppendClusterSample(cs ClusterSample) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	cs.Time = time.Now()
	seq, err := m.nextSeq(ctx, "cluster_samples")
	if err != nil {
		return err
	}
	cs.ID = seq
	_, err = m.col("cluster_samples").InsertOne(ctx, cs)
	return err
}

func (m *MongoStore) ListClusterSamples(since time.Time) ([]ClusterSample, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	cur, err := m.col("cluster_samples").Find(ctx, bson.M{"ts": bson.M{"$gte": since}},
		options.Find().SetSort(bson.D{{Key: "seq", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []ClusterSample
	err = cur.All(ctx, &out)
	return out, err
}

func (m *MongoStore) PruneClusterSamples(olderThan time.Time) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	_, err := m.col("cluster_samples").DeleteMany(ctx, bson.M{"ts": bson.M{"$lt": olderThan}})
	return err
}

func (m *MongoStore) GetSSOConfig() (SSOConfig, error) {
	ctx, cancel := ctxTimeout()
	defer cancel()
	var c SSOConfig
	err := m.col("sso_config").FindOne(ctx, bson.M{"_id": SSOConfigID}).Decode(&c)
	return c, notFound(err)
}

func (m *MongoStore) PutSSOConfig(c SSOConfig) error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	c.ID = SSOConfigID
	_, err := m.col("sso_config").ReplaceOne(ctx, bson.M{"_id": SSOConfigID}, c, options.Replace().SetUpsert(true))
	return err
}

// Ping is used by the admin /health endpoint to verify the MongoDB
// connection is actually alive, not just that the process is running.
func (m *MongoStore) Ping() error {
	ctx, cancel := ctxTimeout()
	defer cancel()
	return m.client.Ping(ctx, nil)
}
