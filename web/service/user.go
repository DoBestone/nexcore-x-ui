package service

import (
	"errors"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/logger"
	"x-ui/util/crypto"

	"gorm.io/gorm"
)

type UserService struct {
}

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
	return db.Model(model.User{}).
		Where("id = ?", id).
		Update("username", username).
		Update("password", hash).
		Error
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
	return db.Save(user).Error
}
