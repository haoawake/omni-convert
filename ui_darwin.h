// macOS 界面：Objective-C（ui_darwin.m）这边提供的函数。排版和逻辑都在 Go（ui_darwin.go）里，
// 这里只负责创建 AppKit 控件、改它们的属性，以及把点击、输入、拖放等事件转回 Go（goXxx 函数）。
// 坐标都是「点」，原点在窗口内容区的左上角，往下为正（和 Windows 版一样）。
#ifndef OMNI_UI_DARWIN_H
#define OMNI_UI_DARWIN_H

// 控件种类
enum {
	UI_LABEL = 1, // 一行文字（太长时末尾显示省略号）
	UI_WRAP,      // 会自动换行的文字
	UI_BUTTON,    // 普通按钮
	UI_PRIMARY,   // 蓝色主按钮（开始转换）
	UI_DANGER,    // 红色按钮（停止）
	UI_CHIP,      // 「转成 xx」按钮：选中的那个是蓝色
	UI_POPUP,     // 下拉选择
	UI_PULLDOWN,  // 下拉菜单（标题固定，选了某一项就执行，比如「常用尺寸…」）
	UI_SEGMENT,   // 分段按钮，选一个
	UI_TEXT,      // 文字输入框
	UI_NUMBER,    // 只能输入数字的输入框
	UI_SECURE,    // 密码输入框
	UI_CHECK,     // 勾选框
	UI_IMAGE,     // 系统图标（SF Symbols）
	UI_CARD,      // 白色圆角卡片（深色模式下是深灰）
	UI_BANNER,    // 浅橙色的提醒条
	UI_TOAST,     // 底栏上的提示条（ui_on 为 1 时是红色的出错样式）
	UI_SCROLL,    // 可以滚动的区域，子控件放在里面（parent 填它的编号）
	UI_LINK,      // 文字链接样式的按钮
	UI_DROPZONE,  // 虚线框的拖放区域
	UI_LIST,      // 文件列表（整个程序只有一个）
	UI_SPINNER,   // 小的转圈
};

// 字体
enum { UI_FONT_BODY = 0, UI_FONT_BOLD, UI_FONT_SMALL, UI_FONT_TITLE, UI_FONT_HEADLINE };

// 文字颜色（前几个和 Go 里的 tone 一一对应）
enum { UI_TONE_TEXT = 0, UI_TONE_TEXT2, UI_TONE_TEXT3, UI_TONE_ACCENT, UI_TONE_OK, UI_TONE_DANGER, UI_TONE_WARN };

// 文件列表的一行（字符串由 Go 用 malloc 分配，Objective-C 用完后 free）
typedef struct {
	char *name, *path, *size, *state, *result;
	int nameTone, stateTone, resultTone;
	double frac; // 进度 0~1，小于 0 表示说不准
	int running; // 1 = 画进度条
} UIRow;

// 程序和窗口
void ui_init(void);
void ui_window(const char *title, double w, double h, double minW, double minH);
void ui_run(void);
void ui_quit(void);
void ui_set_title(const char *title);
void ui_content_size(double *w, double *h);
double ui_top_inset(void); // 标题栏占掉的高度（内容一直铺到窗口顶上）

// 控件
int ui_new(int kind, int parent);
void ui_remove(int id);
void ui_frame(int id, double x, double y, double w, double h);
void ui_text(int id, const char *s);
void ui_placeholder(int id, const char *s);
void ui_font(int id, int font);
void ui_tone(int id, int tone);
void ui_on(int id, int on);
void ui_enabled(int id, int on);
void ui_hidden(int id, int hidden);
void ui_items(int id, const char *items, int selected); // items 用 \n 分开，"-" 是分隔线
void ui_symbol(int id, const char *name, double size);
void ui_tooltip(int id, const char *s);
void ui_fit(int id, double *w, double *h); // 控件合适的大小
void ui_measure(const char *s, int font, double maxWidth, double *w, double *h);
void ui_doc_height(int scroll, double h);
double ui_doc_width(int scroll);

// 左边的分类和右边的文件列表
void ui_sidebar(const char *titles, const char *symbols, const char *badges, int selected);
void ui_list_reload(void);
void ui_list_refresh(void);
int ui_list_selection(int *rows, int max);
void ui_list_select(int row);

// 对话框和系统功能
int ui_alert(int style, const char *title, const char *msg, const char *detail, const char *buttons);
char *ui_open_panel(const char *title, const char *exts);
char *ui_choose_folder(const char *title, const char *start);
void ui_open_path(const char *path);
void ui_reveal(const char *path);
void ui_open_url(const char *url);
char *ui_paste_files(void);
void ui_copy_text(const char *s);
void ui_post(int what); // 任何线程都能调用：回到主线程后调用 goPosted(what)
void ui_timer(int which, double seconds);
void ui_timer_stop(int which);
void ui_keep_awake(int on);
void ui_dock(double frac, const char *badge);
void ui_attention(void);
void ui_free(char *p);

// 截图模式
void ui_pump(double seconds);
void ui_dark(int dark);
int ui_save_png(const char *path);
void ui_window_size(double w, double h);

#endif
