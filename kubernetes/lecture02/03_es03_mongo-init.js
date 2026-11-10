// Run once via: kubectl exec -i mongo-0 -- mongo --quiet < 03_es03_mongo-init.js
assert.commandWorked(rs.initiate({
  _id: "MainRepSet",
  members: [
    { _id: 0, host: "mongo-0.mongo.default.svc.cloudlab.internal:27017" },
    { _id: 1, host: "mongo-1.mongo.default.svc.cloudlab.internal:27017" },
    { _id: 2, host: "mongo-2.mongo.default.svc.cloudlab.internal:27017" }
  ]
}));
