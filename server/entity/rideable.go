package entity

import (
	"github.com/go-gl/mathgl/mgl32"
)

type Rideable interface {
	// SeatPositions returns the possible seat positions for an entity in the order that they will be filled.
	SeatPositions() []mgl32.Vec3
	// Riders returns a slice entities that are currently riding an entity in the order that they were added.
	Riders() []Rider
	// AddRider adds a rider to the entity.
	AddRider(e Rider)
	// RemoveRider removes a rider from the entity.
	RemoveRider(e Rider)
	// Move moves the entity using the given vector, yaw, and pitch.
	Move(vector mgl32.Vec2, yaw, pitch float32)
}
