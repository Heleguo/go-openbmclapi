/**
 * OpenBmclAPI (Golang Edition)
 * Copyright (C) 2024 Kevin Z <zyxkad@gmail.com>
 * All rights reserved
 *
 *  This program is free software: you can redistribute it and/or modify
 *  it under the terms of the GNU Affero General Public License as published
 *  by the Free Software Foundation, either version 3 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU Affero General Public License for more details.
 *
 *  You should have received a copy of the GNU Affero General Public License
 *  along with this program.  If not, see <https://www.gnu.org/licenses/>.
 */

package database

import (
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/LiterMC/go-openbmclapi/utils"
)

var (
	ErrStopIter = errors.New("stop iteration")
	ErrNotFound = errors.New("no record was found")
	ErrExists   = errors.New("record's key was already exists")
)

type DB interface {
	// Cleanup will release any release that the database created
	// No operation should be executed during or after cleanup
	Cleanup() (err error)

	ValidJTI(jti string) (bool, error)
	AddJTI(jti string, expire time.Time) error
	RemoveJTI(jti string) error

	// You should not edit the record pointer
	GetFileRecord(path string) (*FileRecord, error)
	SetFileRecord(FileRecord) error
	RemoveFileRecord(path string) error
	// if the callback returns ErrStopIter, ForEach must immediately stop and returns a nil error
	// the callback should not edit the record pointer
	ForEachFileRecord(cb func(*FileRecord) error) error

	GetSubscribe(user string, client string) (*SubscribeRecord, error)
	SetSubscribe(SubscribeRecord) error
	RemoveSubscribe(user string, client string) error
	ForEachSubscribe(cb func(*SubscribeRecord) error) error

	GetEmailSubscription(user string, addr string) (*EmailSubscriptionRecord, error)
	AddEmailSubscription(EmailSubscriptionRecord) error
	UpdateEmailSubscription(EmailSubscriptionRecord) error
	RemoveEmailSubscription(user string, addr string) error
	ForEachEmailSubscription(cb func(*EmailSubscriptionRecord) error) error
	ForEachUsersEmailSubscription(user string, cb func(*EmailSubscriptionRecord) error) error
	ForEachEnabledEmailSubscription(cb func(*EmailSubscriptionRecord) error) error

	GetWebhook(user string, id uuid.UUID) (*WebhookRecord, error)
	AddWebhook(WebhookRecord) error
	UpdateWebhook(WebhookRecord) error
	UpdateEnableWebhook(user string, id uuid.UUID, enabled bool) error
	RemoveWebhook(user string, id uuid.UUID) error
	ForEachWebhook(cb func(*WebhookRecord) error) error
	ForEachUsersWebhook(user string, cb func(*WebhookRecord) error) error
	ForEachEnabledWebhook(cb func(*WebhookRecord) error) error
}

type FileRecord struct {
	Path string
	Hash string
	Size int64
}
