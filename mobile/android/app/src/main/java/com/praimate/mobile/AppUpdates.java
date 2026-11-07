package com.praimate.mobile;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageInfo;
import android.content.pm.PackageManager;
import android.content.pm.Signature;
import android.net.Uri;
import android.os.Build;
import android.provider.Settings;
import android.widget.LinearLayout;
import android.widget.ProgressBar;
import android.widget.TextView;
import android.widget.Toast;
import androidx.core.content.FileProvider;
import org.json.JSONObject;
import java.io.File;
import java.util.Arrays;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.atomic.AtomicBoolean;

/** GitHub updates belong to the Android client, never to a connected server. */
public final class AppUpdates {
    public static final int INSTALL_PERMISSION=16;
    private static final AtomicBoolean busy=new AtomicBoolean();
    private final Activity activity;
    private final ExecutorService io;
    private final SharedPreferences preferences;
    private AlertDialog downloadDialog;
    private ReleaseUpdateClient downloadClient;
    private AtomicBoolean downloadCancelled;
    public AppUpdates(Activity activity,ExecutorService io){this.activity=activity;this.io=io;preferences=activity.getSharedPreferences("app-updates",0);}
    public boolean automatic(){return preferences.getBoolean("automatic",false);}
    public void automatic(boolean enabled){preferences.edit().putBoolean("automatic",enabled).apply();}
    public void close(){if(downloadCancelled!=null)downloadCancelled.set(true);if(downloadClient!=null)downloadClient.cancel();if(downloadDialog!=null)downloadDialog.dismiss();}
    private boolean alive(){return !activity.isFinishing()&&!activity.isDestroyed();}
    private File apk(){return new File(activity.getCacheDir(),"updates/update.apk");}
    public void onOpen(){if(automatic()&&System.currentTimeMillis()-preferences.getLong("automaticAttempt",0)>24*60*60*1000L){preferences.edit().putLong("automaticAttempt",System.currentTimeMillis()).apply();check(true);}}
    public void check(boolean silent){
        if(!busy.compareAndSet(false,true)){if(!silent)Toast.makeText(activity,"An update check or download is already running",Toast.LENGTH_SHORT).show();return;}
        if(!silent)Toast.makeText(activity,"Checking GitHub for Android updates…",Toast.LENGTH_SHORT).show();
        io.execute(()->{
            try{
                ReleaseUpdateClient.Info info;
                long age=System.currentTimeMillis()-preferences.getLong("checkedAt",0);
                if(age>=0&&age<60000){String cache=preferences.getString("candidate","");info=cache.isEmpty()?null:ReleaseUpdateClient.Info.fromJSON(new JSONObject(cache));if(info!=null&&!UpdatePolicy.newer(info.tag,BuildConfig.VERSION_NAME))info=null;}
                else{info=new ReleaseUpdateClient(BuildConfig.VERSION_NAME,BuildConfig.DEBUG).check();preferences.edit().putLong("checkedAt",System.currentTimeMillis()).putString("candidate",info==null?"":info.json().toString()).apply();}
                ReleaseUpdateClient.Info found=info;
                activity.runOnUiThread(()->{if(!alive())return;if(found==null){if(!silent)new AlertDialog.Builder(activity).setTitle("PrAImate "+BuildConfig.VERSION_NAME).setMessage("No newer compatible Android update is available.").setNegativeButton("Close",null).setPositiveButton("Open Releases",(d,w)->openReleases()).show();}else new AlertDialog.Builder(activity).setTitle("Android update available · "+found.tag).setMessage("Download the APK from GitHub and verify its SHA-256 and signing certificate. Android will ask before installing. Server tasks keep running.").setNegativeButton("Later",null).setNeutralButton("Open Releases",(d,w)->openReleases()).setPositiveButton("Download update",(d,w)->download(found)).show();});
            }catch(Exception error){activity.runOnUiThread(()->{if(alive()&&!silent)new AlertDialog.Builder(activity).setTitle("Update check unavailable").setMessage(message(error)).setNegativeButton("Close",null).setPositiveButton("Open Releases",(d,w)->openReleases()).show();});}
            finally{busy.set(false);}
        });
    }
    private void openReleases(){try{activity.startActivity(new Intent(Intent.ACTION_VIEW,Uri.parse(UpdatePolicy.WEB+"/releases")));}catch(RuntimeException ignored){Toast.makeText(activity,"No browser is available",Toast.LENGTH_LONG).show();}}
    private static String message(Exception e){if(e instanceof IllegalArgumentException||e instanceof IllegalStateException||e instanceof java.io.IOException)return e.getMessage()==null?"Try again later or open GitHub Releases.":e.getMessage();return "Update verification failed. Download a compatible APK from GitHub Releases.";}
    private void download(ReleaseUpdateClient.Info info){
        if(!busy.compareAndSet(false,true))return;
        ReleaseUpdateClient client=new ReleaseUpdateClient(BuildConfig.VERSION_NAME,BuildConfig.DEBUG);AtomicBoolean cancelled=new AtomicBoolean();
        LinearLayout body=new LinearLayout(activity);body.setOrientation(LinearLayout.VERTICAL);body.setPadding(32,20,32,20);
        TextView detail=new TextView(activity);detail.setText(R.string.update_downloading);body.addView(detail);ProgressBar progress=new ProgressBar(activity,null,android.R.attr.progressBarStyleHorizontal);progress.setMax(100);body.addView(progress);
        AlertDialog dialog=new AlertDialog.Builder(activity).setTitle("PrAImate "+info.tag).setView(body).setNegativeButton("Cancel",(d,w)->{cancelled.set(true);client.cancel();}).create();dialog.setOnCancelListener(d->{cancelled.set(true);client.cancel();});dialog.show();
        downloadDialog=dialog;downloadClient=client;downloadCancelled=cancelled;
        io.execute(()->{
            try{
                long[] last={0};client.download(info,apk(),cancelled,(n,total)->{if(n-last[0]<262144&&n!=total)return;last[0]=n;activity.runOnUiThread(()->{if(!alive())return;progress.setIndeterminate(total<=0);if(total>0)progress.setProgress((int)(100*n/total));detail.setText(String.format(java.util.Locale.ROOT,"%.1f MiB downloaded",n/1048576.0));});});
                verify(info);if(cancelled.get())return;
                preferences.edit().putString("pending",info.json().toString()).apply();
                activity.runOnUiThread(()->{if(alive())install();});
            }catch(Exception error){if(apk().exists())apk().delete();activity.runOnUiThread(()->{if(alive()&&!cancelled.get())new AlertDialog.Builder(activity).setTitle("Update not installed").setMessage(message(error)).setPositiveButton("Close",null).show();});}
            finally{busy.set(false);activity.runOnUiThread(()->{if(alive())dialog.dismiss();});}
        });
    }
    @SuppressWarnings("deprecation")
    private void verify(ReleaseUpdateClient.Info info)throws Exception{
        if(!ReleaseUpdateClient.hash(apk()).equalsIgnoreCase(info.sha256))throw new IllegalStateException("APK checksum verification failed");
        PackageManager manager=activity.getPackageManager();int flags=Build.VERSION.SDK_INT>=28?PackageManager.GET_SIGNING_CERTIFICATES:PackageManager.GET_SIGNATURES;
        PackageInfo archive=manager.getPackageArchiveInfo(apk().getAbsolutePath(),flags),current=manager.getPackageInfo(activity.getPackageName(),flags);
        if(archive==null||!activity.getPackageName().equals(archive.packageName))throw new IllegalStateException("The APK is for a different application");
        long av=Build.VERSION.SDK_INT>=28?archive.getLongVersionCode():archive.versionCode,cv=Build.VERSION.SDK_INT>=28?current.getLongVersionCode():current.versionCode;
        if(av<=cv)throw new IllegalStateException("The APK is not newer than this installation");
        Signature[] a=Build.VERSION.SDK_INT>=28?archive.signingInfo==null?null:archive.signingInfo.getApkContentsSigners():archive.signatures;
        Signature[] c=Build.VERSION.SDK_INT>=28?current.signingInfo==null?null:current.signingInfo.getApkContentsSigners():current.signatures;
        if(a==null||c==null||a.length!=c.length||!new java.util.HashSet<>(Arrays.asList(a)).equals(new java.util.HashSet<>(Arrays.asList(c))))throw new IllegalStateException("APK signing certificate does not match this installation. The existing app and its profiles were preserved.");
    }
    public void install(){
        if(!alive())return;
        if(!activity.getPackageManager().canRequestPackageInstalls()){
            new AlertDialog.Builder(activity).setTitle("Allow PrAImate updates").setMessage("Android requires permission to install updates from this app. Enable it on the next screen, then return to PrAImate.").setNegativeButton("Later",null).setPositiveButton("Open Android settings",(d,w)->{try{activity.startActivityForResult(new Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES,Uri.parse("package:"+activity.getPackageName())),INSTALL_PERMISSION);}catch(RuntimeException error){Toast.makeText(activity,"Open Android settings to allow app updates",Toast.LENGTH_LONG).show();}}).show();return;
        }
        io.execute(()->{
            try{String raw=preferences.getString("pending","");if(raw.isEmpty())return;ReleaseUpdateClient.Info info=ReleaseUpdateClient.Info.fromJSON(new JSONObject(raw));verify(info);
                activity.runOnUiThread(()->{if(!alive())return;try{Uri uri=FileProvider.getUriForFile(activity,activity.getPackageName()+".updates",apk());Intent intent=new Intent(Intent.ACTION_VIEW).setDataAndType(uri,"application/vnd.android.package-archive").addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);activity.startActivity(intent);preferences.edit().remove("pending").apply();}catch(RuntimeException error){Toast.makeText(activity,"No Android package installer is available",Toast.LENGTH_LONG).show();}});
            }catch(Exception error){activity.runOnUiThread(()->{if(alive())Toast.makeText(activity,message(error),Toast.LENGTH_LONG).show();});}
        });
    }
}
