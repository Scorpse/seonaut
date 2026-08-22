package passwordhash

import "golang.org/x/crypto/bcrypt"

func Hash(password []byte) (string, error) {
	hash, err := bcrypt.GenerateFromPassword(password, bcrypt.DefaultCost)
	return string(hash), err
}

func Verify(hash string, password []byte) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), password)
}
