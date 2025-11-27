package models

// UserTagWithFrequency representa un tag de usuario con su frecuencia
type UserTagWithFrequency struct {
	Tag       string
	Frequency int
}

// UserDoc representa un usuario en MongoDB
type UserDoc struct {
	UserID          int      `json:"userId"`
	UIdx            *int     `json:"uIdx,omitempty"`
	FirstName       string   `json:"firstName"`
	LastName        string   `json:"lastName"`
	Username        string   `json:"username"`
	Email           string   `json:"email"`
	PasswordHash    string   `json:"passwordHash"`
	Role            string   `json:"role"`
	About           string   `json:"about,omitempty"`
	PreferredGenres []string `json:"preferredGenres,omitempty"`
	CreatedAt       string   `json:"createdAt"`
	UpdatedAt       string   `json:"updatedAt"`
}
