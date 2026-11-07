package com.praimate.mobile;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.os.Build;
import android.os.IBinder;
import org.json.JSONObject;
import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.net.HttpURLConnection;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.atomic.AtomicLong;

/** Optional, user-started monitoring. Server jobs never depend on this service. */
public final class MonitorService extends Service {
    private static final String CONNECTION="praimate-connection",ACTIVITY="praimate-activity";
    private static final int ONGOING=1;
    private final AtomicLong generation=new AtomicLong();
    private volatile HttpURLConnection connection;
    private Thread reader;
    private String profile="";
    private static volatile String monitoredProfile="";
    public static boolean monitoring(String profile){return !profile.isEmpty()&&profile.equals(monitoredProfile);}

    @Override public void onCreate(){
        super.onCreate();NotificationManager notifications=getSystemService(NotificationManager.class);
        notifications.createNotificationChannel(new NotificationChannel(CONNECTION,"Server connection",NotificationManager.IMPORTANCE_LOW));
        notifications.createNotificationChannel(new NotificationChannel(ACTIVITY,"Tasks and approvals",NotificationManager.IMPORTANCE_DEFAULT));
    }
    private Notification ongoing(String message){
        PendingIntent open=PendingIntent.getActivity(this,0,new Intent(this,MainActivity.class).putExtra("profile",profile),PendingIntent.FLAG_IMMUTABLE|PendingIntent.FLAG_UPDATE_CURRENT);
        PendingIntent stop=PendingIntent.getService(this,0,new Intent(this,MonitorService.class).setAction("stop"),PendingIntent.FLAG_IMMUTABLE|PendingIntent.FLAG_UPDATE_CURRENT);
        return new Notification.Builder(this,CONNECTION).setSmallIcon(R.drawable.ic_notification).setContentTitle("PrAImate connected").setContentText(message).setContentIntent(open).setOngoing(true).setVisibility(Notification.VISIBILITY_PRIVATE).addAction(new Notification.Action.Builder(null,"Disconnect monitor",stop).build()).build();
    }
    @Override public int onStartCommand(Intent intent,int flags,int startID){
        if(intent==null||"stop".equals(intent.getAction())){stopSelf();return START_NOT_STICKY;}
        String selected=intent.getStringExtra("profile");if(selected==null){stopSelf();return START_NOT_STICKY;}
        profile=selected;
        monitoredProfile=selected;
        if(Build.VERSION.SDK_INT>=29)startForeground(ONGOING,ongoing("Monitoring server activity"),ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC);else startForeground(ONGOING,ongoing("Monitoring server activity"));
        long current=generation.incrementAndGet();if(connection!=null)connection.disconnect();if(reader!=null)reader.interrupt();
        reader=new Thread(()->monitor(current,selected),"praimate-monitor");reader.start();return START_NOT_STICKY;
    }
    private void monitor(long current,String selected){
        Vault vault=new Vault(this);String cursor="";int failures=0;
        while(generation.get()==current){
            HttpURLConnection stream=null;
            try{
                stream=new ServerConnection(vault).open(selected,"/v1/events"+(cursor.isEmpty()?"":"?cursor="+java.net.URLEncoder.encode(cursor,"UTF-8")),"GET",true);connection=stream;
                int code=ServerConnection.responseCode(stream);
                if(code==401||code==403){notice("Device access expired","Open PrAImate to pair this device again.","#page=dashboard");stopSelf();return;}
                if(code!=200)throw new java.io.IOException("Event connection unavailable");
                getSystemService(NotificationManager.class).notify(ONGOING,ongoing("Monitoring server activity"));failures=0;
                try(BufferedReader input=new BufferedReader(new InputStreamReader(stream.getInputStream(),StandardCharsets.UTF_8))){
                    String line;while(generation.get()==current&&(line=readLine(input))!=null){
                        if(!line.startsWith("data: "))continue;JSONObject event=new JSONObject(line.substring(6));if(event.has("id"))cursor=event.getString("id");
                        String name=event.optString("name");JSONObject payload=event.optJSONObject("payload");if(payload==null)continue;
                        if(name.equals("praimate:chat-finished"))notice(payload.optString("error").isEmpty()?"Conversation finished":"Conversation needs attention","Open PrAImate to review the result.","#page=chats&chat="+encode(payload.optString("chatId")));
                        else if(name.equals("praimate:approval"))notice("Approval requested","A server task is waiting for your decision.","#page="+(payload.optString("chatId").startsWith("workers-")?"workers&worker=":"chats&chat=")+encode(payload.optString("chatId")));
                        else if(name.equals("praimate:worker-update")&&java.util.Arrays.asList("completed","failed","cancelled").contains(payload.optString("status")))notice("Worker "+payload.optString("status"),"Open PrAImate to review the run.","#page=workers&worker="+encode(payload.optString("id")));
                    }
                }
            }catch(Exception error){if(generation.get()==current){if(error instanceof AccessSession.Required){notice("Cloudflare sign-in expired","Open PrAImate and sign in to Cloudflare Access. Server tasks continue.","#page=dashboard");stopSelf();return;}getSystemService(NotificationManager.class).notify(ONGOING,ongoing("Reconnecting; server tasks continue independently"));}}
            finally{if(stream!=null)stream.disconnect();}
            if(generation.get()!=current)return;
            try{Thread.sleep(Math.min(30000,1500L<<Math.min(failures++,4)));}catch(InterruptedException ignored){return;}
        }
    }
    private static String encode(String value){try{return java.net.URLEncoder.encode(value,"UTF-8");}catch(Exception ignored){return "";}}
    private static String readLine(BufferedReader reader)throws java.io.IOException{
        StringBuilder line=new StringBuilder();int value;
        while((value=reader.read())!=-1){if(value=='\n')return line.toString();if(value!='\r')line.append((char)value);if(line.length()>350000)throw new java.io.IOException("Event exceeds limit");}
        return line.length()==0?null:line.toString();
    }
    private void notice(String title,String message,String route){
        int id=2+((profile+route).hashCode()&0x7fff);PendingIntent open=PendingIntent.getActivity(this,id,new Intent(this,MainActivity.class).setData(android.net.Uri.parse("praimate://notification/"+encode(profile)+"/"+encode(route))).putExtra("profile",profile).putExtra("route",route),PendingIntent.FLAG_IMMUTABLE|PendingIntent.FLAG_UPDATE_CURRENT);
        getSystemService(NotificationManager.class).notify(id,new Notification.Builder(this,ACTIVITY).setSmallIcon(R.drawable.ic_notification).setContentTitle(title).setContentText(message).setContentIntent(open).setAutoCancel(true).setOnlyAlertOnce(true).setVisibility(Notification.VISIBILITY_PRIVATE).build());
    }
    @Override public void onTimeout(int startID,int foregroundServiceType){stopSelf();}
    @Override public void onDestroy(){monitoredProfile="";generation.incrementAndGet();if(connection!=null)connection.disconnect();if(reader!=null)reader.interrupt();stopForeground(STOP_FOREGROUND_REMOVE);super.onDestroy();}
    @Override public IBinder onBind(Intent intent){return null;}
}
