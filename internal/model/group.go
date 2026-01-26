package model

type GroupMode int

const (
	GroupModeRoundRobin GroupMode = 1 // 杞锛氫緷娆″惊鐜€夋嫨娓犻亾
	GroupModeRandom     GroupMode = 2 // 闅忔満锛氭瘡娆￠殢鏈洪€夋嫨涓€涓笭閬?
	GroupModeFailover   GroupMode = 3 // 鏁呴殰杞Щ锛氭寜浼樺厛绾ч€夋嫨锛屽け璐ユ椂闄嶇骇鍒颁笅涓€涓?
	GroupModeWeighted   GroupMode = 4 // 鍔犳潈鍒嗛厤锛氭寜浼樻潈閲嶅垎閰嶆祦閲?
)

type Group struct {
	ID                int         `json:"id" gorm:"primaryKey"`
	Name              string      `json:"name" gorm:"unique;not null"`
	Mode              GroupMode   `json:"mode" gorm:"not null"`
	MatchRegex        string      `json:"match_regex"`
	Tag               string      `json:"tag" gorm:"index"`
	FirstTokenTimeOut int         `json:"first_token_time_out"` // 鍗曚釜娓犻亾棣栦釜Token鍝嶅簲瓒呮椂鏃堕棿(绉?
	Items             []GroupItem `json:"items,omitempty" gorm:"foreignKey:GroupID"`
}

type GroupItem struct {
	ID        int    `json:"id" gorm:"primaryKey"`
	GroupID   int    `json:"group_id" gorm:"not null;index:idx_group_channel_model,unique"` // 鍒涘缓鏃朵笉鎼哄甫姝ゅ瓧娈?鏇存柊鏃堕渶瑕?
	ChannelID int    `json:"channel_id" gorm:"not null;index:idx_group_channel_model,unique"`
	ModelName string `json:"model_name" gorm:"not null;index:idx_group_channel_model,unique"`
	Priority  int    `json:"priority"`
	Weight    int    `json:"weight"`
}

// GroupUpdateRequest 鍒嗙粍鏇存柊璇锋眰 - 浠呭寘鍚彉鏇寸殑鏁版嵁
type GroupUpdateRequest struct {
	ID                int                      `json:"id" binding:"required"`
	Name              *string                  `json:"name,omitempty"`                 // 浠呭湪鍚嶇О鍙樻洿鏃跺彂閫?
	Mode              *GroupMode               `json:"mode,omitempty"`                 // 浠呭湪妯″紡鍙樻洿鏃跺彂閫?
	MatchRegex        *string                  `json:"match_regex,omitempty"`          // 浠呭湪鍖归厤姝ｅ垯鍙樻洿鏃跺彂閫?
	FirstTokenTimeOut *int                     `json:"first_token_time_out,omitempty"` // 浠呭湪瓒呮椂鍙樻洿鏃跺彂閫?绉?
	Tag               *string                  `json:"tag,omitempty"`                  // 鍒嗙粍鏍囩锛岄渶瑕佸叏灞€鍞竴
	ItemsToAdd        []GroupItemAddRequest    `json:"items_to_add,omitempty"`         // 鏂板鐨?items
	ItemsToUpdate     []GroupItemUpdateRequest `json:"items_to_update,omitempty"`      // 鏇存柊鐨?items (priority 鍙樻洿)
	ItemsToDelete     []int                    `json:"items_to_delete,omitempty"`      // 鍒犻櫎鐨?item IDs
}

// GroupItemAddRequest 鏂板 item 璇锋眰
type GroupItemAddRequest struct {
	ChannelID int    `json:"channel_id" binding:"required"`
	ModelName string `json:"model_name" binding:"required"`
	Priority  int    `json:"priority,omitempty"`
	Weight    int    `json:"weight,omitempty"`
}

// GroupItemUpdateRequest 鏇存柊 item 璇锋眰
type GroupItemUpdateRequest struct {
	ID       int `json:"id" binding:"required"`
	Priority int `json:"priority,omitempty"`
	Weight   int `json:"weight,omitempty"`
}
type GroupIDAndLLMName struct {
	ChannelID int
	ModelName string
}
