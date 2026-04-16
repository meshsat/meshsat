package gateway

import "meshsat/internal/database"

// databasePackageRouting is a type alias so the zigbee gateway can refer to
// database.ZigBeeDeviceRouting without taking a direct dependency in the
// router struct (keeps the seam clean for tests). [MESHSAT-509]
type databasePackageRouting = database.ZigBeeDeviceRouting
