package store

// Image is a built image tag kept for rollbacks.
type Image struct {
	ID           string
	AppID        string
	Tag          string
	DeploymentID string
	SizeBytes    int64
	CreatedAt    string
}

// CreateImage records a built image tag.
func (s *Store) CreateImage(img Image) (Image, error) {
	img.ID = NewID()
	img.CreatedAt = Now()
	_, err := s.db.Exec(
		`INSERT INTO images (id, app_id, tag, deployment_id, size_bytes, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		img.ID, img.AppID, img.Tag, img.DeploymentID, img.SizeBytes, img.CreatedAt,
	)
	return img, err
}

// ListImages returns an app's images, newest first.
func (s *Store) ListImages(appID string) ([]Image, error) {
	rows, err := s.db.Query(
		`SELECT id, app_id, tag, deployment_id, size_bytes, created_at FROM images WHERE app_id = ? ORDER BY created_at DESC`,
		appID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Image
	for rows.Next() {
		var img Image
		if err := rows.Scan(&img.ID, &img.AppID, &img.Tag, &img.DeploymentID, &img.SizeBytes, &img.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, rows.Err()
}

// DeleteImage removes an image record.
func (s *Store) DeleteImage(id string) error {
	_, err := s.db.Exec(`DELETE FROM images WHERE id = ?`, id)
	return err
}
