package com.praimate.mobile;

import java.net.URI;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/** Update URLs are repository-owned, independently of the connected server. */
public final class UpdatePolicy {
    public static final String REPO="sPROFFEs/praimate";
    public static final String WEB="https://github.com/"+REPO;
    public static final String API="https://api.github.com/repos/"+REPO+"/releases?per_page=30";
    private static final Pattern VERSION=Pattern.compile("^(?:server-)?v?(\\d+)\\.(\\d+)\\.(\\d+)(?:-[A-Za-z0-9.-]+)?$");
    private UpdatePolicy(){}
    public static boolean newer(String remote,String installed){
        long[] a=version(remote),b=version(installed);
        for(int i=0;i<3;i++){if(a[i]!=b[i])return a[i]>b[i];}return false;
    }
    public static String androidVersion(String tag,String notes){
        // A release can retain its host binaries while publishing a newer APK.
        Matcher marker=Pattern.compile("<!-- praimate-android-version:([0-9]+\\.[0-9]+\\.[0-9]+) -->").matcher(notes==null?"":notes);
        if(!marker.find())return tag;
        String candidate=marker.group(1);
        if(marker.find())throw new IllegalArgumentException("Ambiguous Android release version");
        version(tag);version(candidate);return candidate;
    }
    private static long[] version(String value){
        Matcher m=VERSION.matcher(value==null?"":value);if(!m.matches())throw new IllegalArgumentException("Invalid release version");
        return new long[]{Long.parseLong(m.group(1)),Long.parseLong(m.group(2)),Long.parseLong(m.group(3))};
    }
    public static String releaseURL(String tag,String name){
        if(!tag.matches("(?:server-)?v\\d+\\.\\d+\\.\\d+(?:-[A-Za-z0-9.-]+)?")||!name.matches("[A-Za-z0-9_.-]+"))throw new IllegalArgumentException("Invalid release asset");
        return WEB+"/releases/download/"+tag+"/"+name;
    }
    public static void trustedURL(String raw){
        try{
            URI u=new URI(raw);String host=u.getHost();
            if(!"https".equals(u.getScheme())||u.getRawUserInfo()!=null||u.getFragment()!=null||(u.getPort()!=-1&&u.getPort()!=443))throw new IllegalArgumentException();
            String path=u.getPath().toLowerCase(java.util.Locale.ROOT),repo=REPO.toLowerCase(java.util.Locale.ROOT);
            boolean allowed="api.github.com".equals(host)&&path.startsWith("/repos/"+repo+"/releases")
                ||"github.com".equals(host)&&path.startsWith("/"+repo+"/releases/")
                ||"release-assets.githubusercontent.com".equals(host)||"objects.githubusercontent.com".equals(host);
            if(!allowed)throw new IllegalArgumentException();
        }catch(Exception invalid){throw new IllegalArgumentException("Update request must use this repository's HTTPS release endpoints");}
    }
    public static String checksum(String manifest,String filename){
        String found=null;
        for(String line:manifest.split("\\r?\\n")){
            String[] parts=line.trim().split("\\s+",2);if(parts.length!=2)continue;
            if(parts[1].replaceFirst("^\\*","").equals(filename)){
                if(found!=null||!parts[0].matches("[a-fA-F0-9]{64}"))throw new IllegalArgumentException("Invalid update checksum manifest");
                found=parts[0].toLowerCase(java.util.Locale.ROOT);
            }
        }
        if(found==null)throw new IllegalArgumentException("This release has no compatible Android update");return found;
    }
}
