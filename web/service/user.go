package service

import (
	"errors"
	"nexcore-x-ui/database"
	"nexcore-x-ui/database/model"
	"nexcore-x-ui/logger"
	"nexcore-x-ui/util/crypto"

	"gorm.io/gorm"
)

type UserService struct {
}

// dummyBcryptHash is a fixed bcrypt hash of an unguessable random string
// at the same cost factor we use for real passwords. CheckUser runs it
// through bcrypt.CompareHashAndPassword whenever the username row is
// missing so the success and failure paths spend the same ~150ms,
// closing the username-enumeration timing oracle that leaks valid logins
// to anyone who can measure server response time. The hash itself
// matches no plaintext anyone can submit (60-byte random input) — even
// an attacker who learns this constant cannot forge a login with it.
const dummyBcryptHash = "$2a$12$FLbwb6C.Cs7Q6SIaqRZzGOPkyN7WcJvF4K/9Gii/GNounkpnmmqMq"

func (s *UserService) GetFirstUser() (*model.User, error) {
	db := database.GetDB()

	user := &model.User{}
	err := db.Model(model.User{}).
		First(user).
		Error
	if err != nil {
		return nil, err
	}
	return user, nil
}

func (s *UserService) CheckUser(username string, password string) *model.User {
	db := database.GetDB()

	user := &model.User{}
	err := db.Model(model.User{}).
		Where("username = ?", username).
		First(user).
		Error
	if err == gorm.ErrRecordNotFound {
		// Constant-time guard: spend the same bcrypt budget on a
		// missing-user response as on a present-but-wrong-password
		// response. Without this, a SELECT-miss returns in microseconds
		// while a hit costs ~150ms (cost-12 bcrypt verify), and any
		// attacker who can time the panel learns which usernames exist.
		_, _ = crypto.VerifyPassword(dummyBcryptHash, password)
		return nil
	} else if err != nil {
		logger.Warning("check user err:", err)
		return nil
	}

	ok, needsRehash := crypto.VerifyPassword(user.Password, password)
	if !ok {
		return nil
	}
	if needsRehash {
		hash, err := crypto.HashPassword(password)
		if err == nil {
			if err := db.Model(model.User{}).Where("id = ?", user.Id).Update("password", hash).Error; err != nil {
				logger.Warning("upgrade legacy plaintext password to bcrypt failed:", err)
			} else {
				user.Password = hash
			}
		}
	}
	return user
}

func (s *UserService) UpdateUser(id int, username string, password string) error {
	db := database.GetDB()
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return err
	}
	if err := db.Model(model.User{}).
		Where("id = ?", id).
		Update("username", username).
		Update("password", hash).
		Error; err != nil {
		return err
	}
	return nil
}

func (s *UserService) UpdateFirstUser(username string, password string) error {
	if username == "" {
		return errors.New("username can not be empty")
	} else if password == "" {
		return errors.New("password can not be empty")
	}
	hash, err := crypto.HashPassword(password)
	if err != nil {
		return err
	}
	db := database.GetDB()
	user := &model.User{}
	err = db.Model(model.User{}).First(user).Error
	if database.IsNotFound(err) {
		user.Username = username
		user.Password = hash
		return db.Model(model.User{}).Create(user).Error
	} else if err != nil {
		return err
	}
	user.Username = username
	user.Password = hash
	if err := db.Save(user).Error; err != nil {
		return err
	}
	return nil
}
