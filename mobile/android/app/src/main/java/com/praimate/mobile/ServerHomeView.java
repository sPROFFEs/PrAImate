package com.praimate.mobile;

import android.content.Context;
import android.content.res.ColorStateList;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.GradientDrawable;
import android.graphics.drawable.RippleDrawable;
import android.os.Build;
import android.text.InputType;
import android.text.TextUtils;
import android.view.Gravity;
import android.view.View;
import android.view.inputmethod.EditorInfo;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.EditText;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import org.json.JSONArray;
import org.json.JSONObject;

/** Native connection screen; credentials and connection policy stay in the host. */
final class ServerHomeView extends ScrollView {
    interface Actions {
        void connect(String id);
        void options(String id);
        void save(String name,String address)throws Exception;
        void checkUpdates();
        void automaticUpdates(boolean enabled);
    }
    private static final int BG=0xff17191e,PANEL=0xff202329,BORDER=0xff363b46;
    private static final int INK=0xffeef0f5,MUTED=0xffa7afbf,ACCENT=0xff99b8ff;
    private final LinearLayout columns,saved,setup;
    private boolean wide;

    ServerHomeView(Context context,JSONArray profiles,String profileError,String version,boolean automatic,Actions actions){
        super(context);
        setFillViewport(true);setBackgroundColor(BG);setClipToPadding(false);
        LinearLayout content=column();content.setPadding(dp(20),dp(24),dp(20),dp(28));addView(content);

        LinearLayout brand=row();brand.setGravity(Gravity.CENTER_VERTICAL);
        ImageView logo=new ImageView(context);logo.setImageResource(R.mipmap.ic_launcher);logo.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);
        LinearLayout.LayoutParams logoSize=new LinearLayout.LayoutParams(dp(52),dp(52));logoSize.setMarginEnd(dp(12));brand.addView(logo,logoSize);
        LinearLayout identity=column();identity.addView(label("PrAImate",23,INK,true));identity.addView(label("Your remote workspace",13,MUTED,false));brand.addView(identity,new LinearLayout.LayoutParams(0,-2,1));
        TextView build=label(version,11,MUTED,false);build.setPadding(dp(9),dp(6),dp(9),dp(6));build.setMaxWidth(dp(100));build.setBackground(shape(PANEL,BORDER,8));brand.addView(build);
        content.addView(brand);
        TextView title=label("Connect to your workspace",27,INK,true);heading(title);add(content,title,28,8);
        add(content,label("Your projects and models stay on your server. Pick a saved connection or add a new one.",15,MUTED,false),0,24);

        columns=row();columns.setGravity(Gravity.TOP);content.addView(columns,new LinearLayout.LayoutParams(-1,-2));
        saved=column();setup=column();columns.addView(saved);columns.addView(setup);
        TextView savedTitle=label("Your servers",18,INK,true);heading(savedTitle);add(saved,savedTitle,0,12);
        if(!profileError.isEmpty())add(saved,label(profileError,14,0xffffb1b7,false),0,12);
        else if(profiles.length()==0){
            LinearLayout empty=column();empty.setPadding(dp(18),dp(20),dp(18),dp(20));empty.setBackground(shape(PANEL,BORDER,16));
            empty.addView(label("No servers yet",17,INK,true));add(empty,label("Add the HTTPS address shown in PrAImate Server to get started. You can pair this device after connecting.",14,MUTED,false),8,0);saved.addView(empty);
        }
        for(int i=0;i<profiles.length();i++){
            JSONObject profile=profiles.optJSONObject(i);if(profile==null)continue;
            String id=profile.optString("id"),name=profile.optString("name"),url=profile.optString("url");boolean paired=profile.optBoolean("paired");
            LinearLayout card=row();card.setGravity(Gravity.CENTER_VERTICAL);card.setBackground(shape(PANEL,BORDER,16));
            LinearLayout details=column();details.setPadding(dp(16),dp(16),dp(8),dp(16));details.setBackground(ripple(PANEL,16));details.setFocusable(true);details.setClickable(true);details.setOnClickListener(view->actions.connect(id));details.setOnLongClickListener(view->{actions.options(id);return true;});
            String status=paired?"Paired device":"Pairing required";details.setContentDescription("Connect to "+name+", "+url+", "+status);
            TextView serverName=label(name,17,INK,true);serverName.setMaxLines(2);serverName.setEllipsize(TextUtils.TruncateAt.END);details.addView(serverName);
            TextView address=label(url,13,MUTED,false);address.setMaxLines(2);address.setEllipsize(TextUtils.TruncateAt.MIDDLE);add(details,address,5,10);
            TextView state=label(status,12,paired?ACCENT:0xffe9c27e,true);state.setPadding(dp(8),dp(4),dp(8),dp(4));state.setBackground(shape(paired?0xff29354d:0xff393126,Color.TRANSPARENT,6));details.addView(state,new LinearLayout.LayoutParams(-2,-2));
            for(int n=0;n<details.getChildCount();n++)details.getChildAt(n).setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);
            card.addView(details,new LinearLayout.LayoutParams(0,-2,1));
            Button options=action("⋮",false,()->actions.options(id));options.setTextSize(24);options.setContentDescription("Options for "+name);LinearLayout.LayoutParams optionSize=new LinearLayout.LayoutParams(dp(48),dp(48));optionSize.setMarginEnd(dp(6));card.addView(options,optionSize);
            add(saved,card,0,10);
        }
        add(saved,label("Server options include certificates, Cloudflare Access and device pairing.",13,MUTED,false),10,0);

        LinearLayout form=column();form.setPadding(dp(18),dp(18),dp(18),dp(18));form.setBackground(shape(PANEL,BORDER,16));setup.addView(form);
        TextView formTitle=label("Add a server",18,INK,true);heading(formTitle);form.addView(formTitle);
        TextView nameLabel=label("Server name",13,INK,true);add(form,nameLabel,18,6);
        EditText name=field("e.g. Home workspace",InputType.TYPE_CLASS_TEXT|InputType.TYPE_TEXT_FLAG_CAP_SENTENCES);nameLabel.setLabelFor(name.getId());form.addView(name,new LinearLayout.LayoutParams(-1,-2));
        TextView urlLabel=label("Server address",13,INK,true);add(form,urlLabel,14,6);
        EditText url=field("https://praimate.example.com",InputType.TYPE_CLASS_TEXT|InputType.TYPE_TEXT_VARIATION_URI);urlLabel.setLabelFor(url.getId());url.setImeOptions(EditorInfo.IME_ACTION_GO);form.addView(url,new LinearLayout.LayoutParams(-1,-2));
        add(form,label("Use the HTTPS origin, including a port if needed. Pair using the one-use code from your server.",13,MUTED,false),8,14);
        Runnable save=()->{try{actions.save(name.getText().toString(),url.getText().toString());}catch(Exception error){url.setError(NativeBridge.publicMessage(error));url.requestFocus();}};
        form.addView(action("Save and connect",true,save),new LinearLayout.LayoutParams(-1,-2));
        url.setOnEditorActionListener((view,action,event)->{if(action!=EditorInfo.IME_ACTION_GO)return false;save.run();return true;});

        View divider=new View(context);divider.setBackgroundColor(BORDER);LinearLayout.LayoutParams line=new LinearLayout.LayoutParams(-1,dp(1));line.topMargin=dp(28);line.bottomMargin=dp(18);content.addView(divider,line);
        LinearLayout updates=row();updates.setGravity(Gravity.CENTER_VERTICAL);TextView updateTitle=label("App updates",16,INK,true);updates.addView(updateTitle,new LinearLayout.LayoutParams(0,-2,1));updates.addView(action("Check for updates",false,actions::checkUpdates));content.addView(updates);
        CheckBox check=new CheckBox(context);check.setText(R.string.update_automatic);check.setTextSize(13);check.setTextColor(MUTED);check.setButtonTintList(ColorStateList.valueOf(ACCENT));check.setMinHeight(dp(48));check.setChecked(automatic);check.setOnCheckedChangeListener((view,enabled)->actions.automaticUpdates(enabled));content.addView(check);
        setColumns(false);
    }

    @Override protected void onSizeChanged(int width,int height,int oldWidth,int oldHeight){super.onSizeChanged(width,height,oldWidth,oldHeight);boolean next=width-getPaddingLeft()-getPaddingRight()>=dp(720);if(next!=wide)setColumns(next);}
    private void setColumns(boolean next){
        wide=next;columns.setOrientation(wide?LinearLayout.HORIZONTAL:LinearLayout.VERTICAL);
        LinearLayout.LayoutParams first=new LinearLayout.LayoutParams(wide?0:-1,-2,wide?1:0);first.setMarginEnd(wide?dp(24):0);saved.setLayoutParams(first);
        LinearLayout.LayoutParams second=new LinearLayout.LayoutParams(wide?0:-1,-2,wide?1:0);second.topMargin=wide?0:dp(24);setup.setLayoutParams(second);
    }
    private int dp(int value){return Math.round(value*getResources().getDisplayMetrics().density);}
    private void heading(TextView view){if(Build.VERSION.SDK_INT>=28)view.setAccessibilityHeading(true);}
    private LinearLayout column(){LinearLayout view=new LinearLayout(getContext());view.setOrientation(LinearLayout.VERTICAL);return view;}
    private LinearLayout row(){LinearLayout view=column();view.setOrientation(LinearLayout.HORIZONTAL);return view;}
    private TextView label(String value,int size,int color,boolean medium){TextView view=new TextView(getContext());view.setText(value);view.setTextSize(size);view.setTextColor(color);view.setLineSpacing(dp(2),1);if(medium)view.setTypeface(Typeface.create("sans-serif-medium",Typeface.NORMAL));return view;}
    private void add(LinearLayout parent,View view,int top,int bottom){LinearLayout.LayoutParams params=new LinearLayout.LayoutParams(-1,-2);params.topMargin=dp(top);params.bottomMargin=dp(bottom);parent.addView(view,params);}
    private EditText field(String hint,int type){EditText field=new EditText(getContext());field.setId(View.generateViewId());field.setSingleLine(true);field.setInputType(type);field.setTextSize(16);field.setTextColor(INK);field.setHintTextColor(MUTED);field.setHint(hint);field.setPadding(dp(12),dp(13),dp(12),dp(13));field.setMinHeight(dp(52));android.graphics.drawable.StateListDrawable background=new android.graphics.drawable.StateListDrawable();background.addState(new int[]{android.R.attr.state_focused},shape(BG,ACCENT,10));background.addState(new int[]{},shape(BG,BORDER,10));field.setBackground(background);return field;}
    private Button action(String title,boolean primary,Runnable callback){Button button=new Button(getContext());button.setText(title);button.setAllCaps(false);button.setTextSize(14);button.setTypeface(Typeface.create("sans-serif-medium",Typeface.NORMAL));button.setTextColor(primary?0xff15213a:ACCENT);button.setMinHeight(dp(48));button.setMinimumHeight(dp(48));button.setPadding(dp(12),dp(8),dp(12),dp(8));button.setStateListAnimator(null);button.setBackground(ripple(primary?ACCENT:Color.TRANSPARENT,10));button.setOnClickListener(view->callback.run());return button;}
    private GradientDrawable shape(int fill,int border,int radius){GradientDrawable shape=new GradientDrawable();shape.setColor(fill);shape.setCornerRadius(dp(radius));if(border!=Color.TRANSPARENT)shape.setStroke(dp(1),border);return shape;}
    private RippleDrawable ripple(int fill,int radius){return new RippleDrawable(ColorStateList.valueOf(0x3399b8ff),shape(fill,Color.TRANSPARENT,radius),shape(Color.WHITE,Color.TRANSPARENT,radius));}
}
